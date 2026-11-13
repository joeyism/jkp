// Package claudeadapter delivers advisory messages to an isolated Claude session.
package claudeadapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joey/jkp/internal/jkpclient"
)

// Config selects the Claude executable, session, and turn limits.
type Config struct {
	ClaudeBinary string
	ForkFrom     string
	ProjectDir   string
	StateFile    string
	Model        string
	EndpointID   string
	TurnTimeout  time.Duration
	Warn         io.Writer
}

// Adapter serializes turns against a background session, never the TUI session.
type Adapter struct {
	mu        sync.Mutex
	cfg       Config
	sidecarID string
	warned    bool
}

// New returns an adapter with defaults applied.
func New(cfg Config) *Adapter {
	if cfg.ClaudeBinary == "" {
		cfg.ClaudeBinary = "claude"
	}
	if cfg.TurnTimeout == 0 {
		cfg.TurnTimeout = 5 * time.Minute
	}
	return &Adapter{cfg: cfg}
}

// IsUnexpandedPlaceholder identifies missing or unexpanded environment values.
func IsUnexpandedPlaceholder(s string) bool {
	return strings.TrimSpace(s) == "" || strings.Contains(s, "${") || strings.HasPrefix(strings.TrimSpace(s), "$")
}

type sessionState struct {
	SessionID string `json:"session_id"`
}

// Handle runs one isolated Claude turn and returns the complete reply text.
func (a *Adapter) Handle(ctx context.Context, ev jkpclient.Event) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	turnCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
	defer cancel()

	if a.sidecarID == "" && a.cfg.StateFile != "" {
		data, err := os.ReadFile(a.cfg.StateFile)
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("claude: load session state: %w", err)
		}
		if err == nil {
			var state sessionState
			if err := json.Unmarshal(data, &state); err != nil {
				return "", fmt.Errorf("claude: decode session state: %w", err)
			}
			a.sidecarID = state.SessionID
		}
	}

	args := []string{"-p"}
	var freshID string
	switch {
	case a.sidecarID != "":
		args = append(args, "--resume", a.sidecarID)
	case !IsUnexpandedPlaceholder(a.cfg.ForkFrom):
		args = append(args, "--resume", a.cfg.ForkFrom, "--fork-session")
	default:
		if !a.warned && a.cfg.Warn != nil {
			fmt.Fprintln(a.cfg.Warn, "jkp: warning: no Claude session ID to fork; starting a fresh background Claude session")
			a.warned = true
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", fmt.Errorf("claude: generate session ID: %w", err)
		}
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		freshID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		args = append(args, "--session-id", freshID)
	}
	args = append(args, "--output-format", "json", "--tools", "", "--disallowedTools", "mcp__*", "--strict-mcp-config", "--safe-mode")
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	args = append(args, "--", fmt.Sprintf("From %s:\n%s\n\n%s", ev.SenderName(), jkpclient.InboundGuidance(), ev.Body))

	cmd := exec.CommandContext(turnCtx, a.cfg.ClaudeBinary, args...)
	if !IsUnexpandedPlaceholder(a.cfg.ProjectDir) {
		cmd.Dir = a.cfg.ProjectDir
	}
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "CLAUDECODE=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if turnCtx.Err() != nil {
			return "", turnCtx.Err()
		}
		return "", fmt.Errorf("claude: %w (stderr: %.1024s; stdout: %.1024s)", err, strings.TrimSpace(stderr.String()), strings.TrimSpace(stdout.String()))
	}
	var resp struct {
		Type      string   `json:"type"`
		Subtype   string   `json:"subtype"`
		IsError   bool     `json:"is_error"`
		SessionID string   `json:"session_id"`
		Result    string   `json:"result"`
		Errors    []string `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("claude: decode response: %w (stdout: %.1024s)", err, stdout.String())
	}
	if resp.IsError {
		return "", fmt.Errorf("claude: %s: %s %s", resp.Subtype, resp.Result, strings.Join(resp.Errors, "; "))
	}
	reply := strings.TrimSpace(resp.Result)
	if reply == "" {
		return "", fmt.Errorf("claude: response contains no reply text")
	}
	if a.sidecarID == "" {
		if resp.SessionID == "" {
			resp.SessionID = freshID
		}
		if resp.SessionID == "" {
			return "", fmt.Errorf("claude: response contains no session ID")
		}
		a.sidecarID = resp.SessionID
		if a.cfg.StateFile != "" {
			if err := a.saveState(); err != nil {
				return "", err
			}
		}
	}
	return reply, nil
}

func (a *Adapter) saveState() error {
	dir := filepath.Dir(a.cfg.StateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("claude: create state directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".claude-session-*")
	if err != nil {
		return fmt.Errorf("claude: create session state: %w", err)
	}
	defer os.Remove(f.Name())
	encodeErr := json.NewEncoder(f).Encode(sessionState{SessionID: a.sidecarID})
	closeErr := f.Close()
	if encodeErr != nil {
		return fmt.Errorf("claude: write session state: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("claude: close session state: %w", closeErr)
	}
	if err := os.Rename(f.Name(), a.cfg.StateFile); err != nil {
		return fmt.Errorf("claude: persist session state: %w", err)
	}
	return nil
}
