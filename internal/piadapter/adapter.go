// Package piadapter bridges jkp inbound events to a persistent pi session and
// routes pi's replies back to jkp.
//
// pi exposes no HTTP server, so the adapter drives it as a subprocess with a
// stable --session-id. Each message is one `pi --print` turn against the same
// session, so conversation context accumulates across turns even though each
// turn is a fresh process.
//
// The adapter runs pi with --no-tools. Remote participant input is advisory
// collaboration, not authorization: a message from another agent or teammate
// must not cause edits or shell commands on the owner's machine. This is an
// enforceable boundary (the tools are not available), not a prompt instruction.
package piadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/joey/jkp/internal/jkpclient"
)

// Config points the adapter at one pi session and one jkp endpoint.
type Config struct {
	// PiBinary is the pi executable. Defaults to "pi" on PATH.
	PiBinary string
	// SessionID is the stable pi session this adapter drives. Context is
	// preserved across turns by this ID.
	SessionID string
	// SessionDir is where pi stores sessions. Optional.
	SessionDir string
	// Provider and Model select the pi model. Example: "anthropic",
	// "anthropic/claude-sonnet-4-5".
	Provider string
	Model    string

	// JKPURL, JKPToken and EndpointID locate the jkp local API.
	JKPURL     string
	JKPToken   string
	EndpointID string

	// TurnTimeout bounds one pi invocation.
	TurnTimeout time.Duration
	// AllowTools opts out of the default --no-tools safety boundary. Setting
	// this lets remote input reach pi's edit and shell tools; do that only
	// when the participants are fully trusted and the consequence is intended.
	AllowTools bool
}

func (c Config) withDefaults() Config {
	if c.PiBinary == "" {
		c.PiBinary = "pi"
	}
	if c.TurnTimeout == 0 {
		c.TurnTimeout = 2 * time.Minute
	}
	return c
}

// Errors reported back to jkp when a turn cannot be completed.
var (
	ErrNoOutput    = errors.New("pi produced no reply text")
	ErrMissingConf = errors.New("incomplete adapter configuration")
)

// Adapter drives one pi session on behalf of one jkp endpoint.
type Adapter struct {
	cfg    Config
	client *jkpclient.Client
	// run turns one message into a reply. It is a field so tests can stub the
	// subprocess without invoking a real model.
	run func(ctx context.Context, message string) (string, error)
}

// New returns an adapter for cfg.
func New(cfg Config) *Adapter {
	cfg = cfg.withDefaults()
	a := &Adapter{
		cfg:    cfg,
		client: jkpclient.New(cfg.JKPURL, cfg.JKPToken, cfg.EndpointID),
	}
	a.run = a.runPi
	return a
}

// WithRunner replaces the subprocess invocation. For tests.
func (a *Adapter) WithRunner(fn func(context.Context, string) (string, error)) *Adapter {
	a.run = fn
	return a
}

// Validate reports whether the configuration is complete enough to run.
func (a *Adapter) Validate() error {
	if a.cfg.SessionID == "" {
		return fmt.Errorf("%w: SessionID is required", ErrMissingConf)
	}
	if a.cfg.JKPURL == "" || a.cfg.JKPToken == "" || a.cfg.EndpointID == "" {
		return fmt.Errorf("%w: JKPURL, JKPToken and EndpointID are required", ErrMissingConf)
	}
	return nil
}

// Run polls jkp for inbound events and dispatches each into the bound pi
// session until ctx is cancelled. One event is handled at a time so replies
// cannot cross-route.
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
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}

		reply, err := a.Handle(ctx, ev)
		if err != nil {
			_ = a.client.Reply(ctx, ev.ID,
				"jkp: the bound session did not produce a reply: "+err.Error())
			continue
		}
		if err := a.client.DispatchReply(ctx, ev.ID, reply); err != nil {
			return err
		}
	}
}

// Handle delivers one event to the bound pi session and returns its reply
// text. It does not send anything back; the caller decides how to route it.
func (a *Adapter) Handle(ctx context.Context, ev jkpclient.Event) (string, error) {
	turnCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	defer cancel()

	text, err := a.run(turnCtx, formatMessage(ev))
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrNoOutput
	}
	return text, nil
}

// formatMessage frames participant input for the session. The framing tells pi
// this is advisory collaboration from a remote participant, so a message never
// masquerades as the owner's instruction, and explains how to reach a peer.
func formatMessage(ev jkpclient.Event) string {
	return fmt.Sprintf("From %s:\n%s\n\n%s", ev.SenderName(), jkpclient.InboundGuidance(), ev.Body)
}

// runPi executes one pi turn against the bound session and returns its text.
func (a *Adapter) runPi(ctx context.Context, message string) (string, error) {
	args := []string{
		"--print",
		"--session-id", a.cfg.SessionID,
	}
	if a.cfg.SessionDir != "" {
		args = append(args, "--session-dir", a.cfg.SessionDir)
	}
	if a.cfg.Provider != "" {
		args = append(args, "--provider", a.cfg.Provider)
	}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	if !a.cfg.AllowTools {
		// Enforce the advisory boundary: remote input cannot reach edit/shell.
		args = append(args, "--no-tools")
	}
	args = append(args, message)

	cmd := exec.CommandContext(ctx, a.cfg.PiBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("pi: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return extractReply(stdout.String()), nil
}

// extractReply returns pi's answer text. pi --print writes the assistant text
// to stdout; anything after a trailing tool transcript is dropped so only the
// final message is sent back to the peer.
func extractReply(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return strings.TrimSpace(strings.Join(lines[max(0, i-20):i+1], "\n"))
		}
	}
	return strings.TrimSpace(out)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
