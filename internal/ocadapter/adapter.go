// Package ocadapter bridges jkp inbound events to an existing OpenCode
// session over its HTTP server API, and routes the session's replies back to
// jkp as explicit, addressed responses.
//
// It is an external process, not an in-process OpenCode plugin: OpenCode's
// server already exposes exact-session prompting and message retrieval, so no
// plugin is required to deliver participant input into a live session.
//
// Correlation: OpenCode generates its own message IDs and ignores a supplied
// one, so each prompt embeds a jkp event marker. The adapter finds the user
// message carrying that marker, then matches the assistant reply through its
// parentID. This stays correct when several prompts are in flight at once.
package ocadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/joey/jkp/internal/jkpclient"
)

// EventMarker prefixes participant input so the adapter can find the exact
// user message it created. It is a routing tag, not part of the message body
// the agent should reason about.
const EventMarker = "[JKP-MSG:"

// Config points the adapter at one OpenCode session and one jkp endpoint.
type Config struct {
	OpenCodeURL string // e.g. http://127.0.0.1:4111
	Username    string // OpenCode basic-auth username (default "opencode")
	Password    string // OpenCode basic-auth password
	SessionID   string // exact existing OpenCode session to bind
	JKPURL      string // e.g. http://127.0.0.1:8443
	JKPToken    string // local API bearer token
	EndpointID  string // jkp endpoint id this adapter serves

	// PollInterval bounds how long one reply waits before re-checking OpenCode.
	PollInterval time.Duration
	// ReplyTimeout bounds how long the adapter waits for a model reply before
	// reporting failure back to jkp.
	ReplyTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Username == "" {
		c.Username = "opencode"
	}
	if c.PollInterval == 0 {
		c.PollInterval = 250 * time.Millisecond
	}
	if c.ReplyTimeout == 0 {
		c.ReplyTimeout = 2 * time.Minute
	}
	return c
}

// Errors returned when the remote session cannot be driven as expected.
var (
	ErrNoReply       = errors.New("no assistant reply before timeout")
	ErrMissingConfig = errors.New("incomplete adapter configuration")
)

// ocMessage is the subset of an OpenCode message the adapter needs.
type ocMessage struct {
	Info struct {
		ID       string `json:"id"`
		Role     string `json:"role"`
		ParentID string `json:"parentID"`
		Finish   string `json:"finish"`
	} `json:"info"`
	Parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"parts"`
}

// Adapter drives one OpenCode session on behalf of one jkp endpoint.
type Adapter struct {
	cfg    Config
	hc     *http.Client
	client *jkpclient.Client
}

// New returns an adapter for cfg. The HTTP client timeout is generous because
// OpenCode's model turns are slow and long-polls are held open.
func New(cfg Config) *Adapter {
	cfg = cfg.withDefaults()
	return &Adapter{
		cfg:    cfg,
		hc:     &http.Client{Timeout: 0}, // per-request contexts bound each call
		client: jkpclient.New(cfg.JKPURL, cfg.JKPToken, cfg.EndpointID),
	}
}

// Validate reports whether the configuration is complete enough to run.
func (a *Adapter) Validate() error {
	if a.cfg.OpenCodeURL == "" || a.cfg.SessionID == "" {
		return fmt.Errorf("%w: OpenCodeURL and SessionID are required", ErrMissingConfig)
	}
	if a.cfg.JKPURL == "" || a.cfg.JKPToken == "" || a.cfg.EndpointID == "" {
		return fmt.Errorf("%w: JKPURL, JKPToken and EndpointID are required", ErrMissingConfig)
	}
	return nil
}

// Run polls jkp for inbound events and dispatches each into the bound OpenCode
// session until ctx is cancelled. One event is handled at a time so replies
// cannot cross-route under concurrency.
func (a *Adapter) Run(ctx context.Context) error {
	if err := a.Validate(); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ev, err := a.client.NextEvent(ctx)
		if err != nil {
			if errors.Is(err, jkpclient.ErrNoWork) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(jkpclient.IdleBackoff):
				}
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			// Transient fetch failure: back off and retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		text, err := a.Handle(ctx, ev)
		if err != nil {
			continue
		}
		decision := jkpclient.Classify(text)
		switch decision.Action {
		case jkpclient.ActionLocalOnly:
			if len(decision.Body) > 240 {
				_ = a.NotifyError(ctx, jkpclient.LocalOnlyNotice(ev.SenderName(), len(decision.Body)))
			}
			continue
		case jkpclient.ActionAckOnly, jkpclient.ActionEmpty:
			continue
		}
		if err := a.client.DispatchReply(ctx, ev.ID, decision.Body); err != nil {
			return err
		}
	}
}

// Handle delivers one event to the bound OpenCode session and returns the
// assistant's reply text. It does not send anything back; the caller decides
// how to route the reply (local API dispatch or direct IRC).
func (a *Adapter) Handle(ctx context.Context, ev jkpclient.Event) (string, error) {
	userMsgID, err := a.inject(ctx, ev)
	if err != nil {
		return "", err
	}
	return a.awaitReply(ctx, userMsgID)
}

// NotifyError injects a one-way delivery notice into the bound OpenCode session.
// It deliberately does not poll for or forward the resulting assistant turn.
func (a *Adapter) NotifyError(ctx context.Context, notice string) error {
	payload := map[string]any{
		"parts": []map[string]any{{"type": "text", "text": notice}},
	}
	return a.ocPost(ctx, fmt.Sprintf("/session/%s/prompt_async", a.cfg.SessionID), payload, nil)
}

// inject submits the participant input as one user message and returns the
// OpenCode message ID it was assigned.
func (a *Adapter) inject(ctx context.Context, ev jkpclient.Event) (string, error) {
	body := fmt.Sprintf("%s%s] %s:\n%s\n\n%s", EventMarker, ev.ID, ev.SenderName(), jkpclient.InboundGuidance(), ev.Body)

	payload := map[string]any{
		"parts": []map[string]any{{"type": "text", "text": body}},
	}
	if err := a.ocPost(ctx, fmt.Sprintf("/session/%s/prompt_async", a.cfg.SessionID), payload, nil); err != nil {
		return "", err
	}

	// OpenCode assigns the message ID; locate it by the embedded marker.
	return a.findUserMessage(ctx, ev.ID)
}

// findUserMessage scans the session for the user message carrying this
// event's marker.
func (a *Adapter) findUserMessage(ctx context.Context, eventID string) (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := a.messages(ctx)
		if err != nil {
			return "", err
		}
		for _, m := range msgs {
			if m.Info.Role != "user" {
				continue
			}
			if strings.Contains(a.text(m), EventMarker+eventID+"]") {
				return m.Info.ID, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(a.cfg.PollInterval):
		}
	}
	return "", errors.New("submitted user message not found in session")
}

// awaitReply waits for the assistant message whose parentID matches the user
// message we submitted, and returns its text.
func (a *Adapter) awaitReply(ctx context.Context, userMsgID string) (string, error) {
	deadline := time.Now().Add(a.cfg.ReplyTimeout)
	for time.Now().Before(deadline) {
		msgs, err := a.messages(ctx)
		if err != nil {
			return "", err
		}
		var last *ocMessage
		for i := range msgs {
			m := &msgs[i]
			if m.Info.Role == "assistant" && m.Info.ParentID == userMsgID {
				last = m
			}
		}
		if last != nil && last.Info.Finish != "" && last.Info.Finish != "tool-calls" {
			if txt := a.text(*last); txt != "" {
				return txt, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(a.cfg.PollInterval):
		}
	}
	return "", ErrNoReply
}

func (a *Adapter) messages(ctx context.Context) ([]ocMessage, error) {
	var out []ocMessage
	err := a.ocGet(ctx, fmt.Sprintf("/session/%s/message", a.cfg.SessionID), &out)
	return out, err
}

func (a *Adapter) text(m ocMessage) string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" && p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func (a *Adapter) ocGet(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(a.cfg.OpenCodeURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(a.cfg.Username, a.cfg.Password)
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("opencode GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *Adapter) ocPost(ctx context.Context, path string, payload, out any) error {
	return a.doPost(ctx, strings.TrimRight(a.cfg.OpenCodeURL, "/")+path,
		a.cfg.Username, a.cfg.Password, true, payload, out)
}

func (a *Adapter) doPost(ctx context.Context, u, user, secret string, basic bool, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if basic {
		req.SetBasicAuth(user, secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST %s: %s: %s", u, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
