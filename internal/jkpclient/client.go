// Package jkpclient is the shared adapter-side view of the jkp local API:
// long-poll for inbound events and post explicit replies. Both the OpenCode
// and pi adapters use it so the adapter contract lives in one place.
package jkpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event is an inbound participant message delivered to one endpoint.
type Event struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Body   string `json:"body"`
	Sender struct {
		Nick    string `json:"nick"`
		Account string `json:"account"`
	} `json:"sender"`
}

// Client talks to the jkp local adapter API.
type Client struct {
	BaseURL    string
	Token      string
	EndpointID string
	// HTTPClient is used for every call; the caller owns its timeouts.
	HTTPClient *http.Client
}

// New returns a client for one endpoint. The HTTP client has no global
// timeout because long-polls are deliberately held open.
func New(baseURL, token, endpointID string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		EndpointID: endpointID,
		HTTPClient: &http.Client{Timeout: 0},
	}
}

// ErrNoWork is returned when a poll completes without an event.
var ErrNoWork = errors.New("no inbound event")

// NextEvent long-polls for one inbound event. It returns ErrNoWork when the
// poll times out empty, so callers can loop without treating idleness as an
// error.
func (c *Client) NextEvent(ctx context.Context) (Event, error) {
	u := fmt.Sprintf("%s/v1/events?endpoint_id=%s",
		c.BaseURL, url.QueryEscape(c.EndpointID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Event{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return Event{}, ErrNoWork
	case http.StatusOK:
		var ev Event
		if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
			return Event{}, err
		}
		return ev, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Event{}, fmt.Errorf("jkp events: %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}
}

// Send lets this endpoint initiate a message to a peer. The peer sees the
// sender as this endpoint, so its reply routes back here. This is the
// agent-to-agent outbound path; an adapter turns its agent's intent to reach a
// peer into a new inbound event for that peer.
func (c *Client) Send(ctx context.Context, toEndpoint, body string) error {
	payload := map[string]any{
		"from_endpoint_id": c.EndpointID,
		"to_endpoint_id":   toEndpoint,
		"body":             body,
	}
	return c.post(ctx, "/v1/send", payload)
}

// Reply posts one explicit reply. jkp derives the conversation coordinate from
// the inbound event, so an adapter cannot redirect a reply elsewhere.
func (c *Client) Reply(ctx context.Context, eventID, body string) error {
	payload := map[string]any{
		"endpoint_id": c.EndpointID,
		"in_reply_to": eventID,
		"body":        body,
	}
	return c.post(ctx, "/v1/reply", payload)
}

func (c *Client) post(ctx context.Context, path string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("jkp %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// DispatchReply executes any peer-directed sends in a harness reply and posts
// the remaining text back to the sender of the original event. When the agent
// only delegated (and left no text for the sender), a short acknowledgement is
// sent instead so the sender is not left waiting indefinitely.
func (c *Client) DispatchReply(ctx context.Context, eventID, harnessReply string) error {
	parsed := ParseReply(harnessReply)
	if parsed.LocalOnly {
		return nil
	}

	var peers []string
	for _, d := range parsed.Sends {
		if err := c.Send(ctx, d.To, d.Body); err != nil {
			return fmt.Errorf("send to %s: %w", d.To, err)
		}
		peers = append(peers, d.To)
	}

	if parsed.Reply != "" {
		return c.Reply(ctx, eventID, parsed.Reply)
	}
	if len(peers) > 0 {
		return c.Reply(ctx, eventID,
			"jkp: forwarded your message to "+strings.Join(peers, ", "))
	}
	return nil
}

// SenderName is the participant label used in messages handed to a harness.
func (e Event) SenderName() string {
	if e.Sender.Nick != "" {
		return e.Sender.Nick
	}
	if e.Sender.Account != "" {
		return e.Sender.Account
	}
	return "participant"
}

// IdleBackoff is how long an adapter waits after a poll with no work.
const IdleBackoff = 50 * time.Millisecond
