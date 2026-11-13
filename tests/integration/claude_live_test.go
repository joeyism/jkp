//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/claudeadapter"
	"github.com/joey/jkp/internal/jkpclient"
)

// TestLiveClaudeSidecarForkAndResume is opt-in and uses only Haiku for all
// three real model calls, including the seed conversation.
func TestLiveClaudeSidecarForkAndResume(t *testing.T) {
	if os.Getenv("JKP_LIVE_CLAUDE") != "1" {
		t.Skip("set JKP_LIVE_CLAUDE=1 to run real Claude calls")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not in PATH")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	seedSessionID := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	var secret [8]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	token := "AURORA-" + hex.EncodeToString(secret[:])
	projDir := t.TempDir()
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelSeed()
	cmd := exec.CommandContext(seedCtx, claude, "-p", "--session-id", seedSessionID,
		"--model", "haiku", "--output-format", "json", "--tools", "",
		"--disallowedTools", "mcp__*", "--strict-mcp-config", "--safe-mode", "--",
		"Remember this exact token: "+token+". Reply with the single word: READY")
	cmd.Dir = projDir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "CLAUDECODE=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed Claude session: %v\n%s", err, out)
	}
	stateFile := filepath.Join(t.TempDir(), "sidecar.json")
	cfg := claudeadapter.Config{
		ClaudeBinary: claude,
		ForkFrom:     seedSessionID,
		ProjectDir:   projDir,
		StateFile:    stateFile,
		Model:        "haiku",
		EndpointID:   "g-live",
		TurnTimeout:  60 * time.Second,
	}
	handle := func(body string) string {
		t.Helper()
		ev := jkpclient.Event{ID: "live-claude", Kind: "dm", Target: "g-live", Body: body}
		ev.Sender.Nick = "live-host"
		// A new adapter on each call proves resume survives a process restart.
		reply, err := claudeadapter.New(cfg).Handle(context.Background(), ev)
		if err != nil {
			t.Fatalf("Claude Handle: %v", err)
		}
		if !strings.Contains(reply, token) {
			t.Fatalf("reply does not remember token %q: %q", token, reply)
		}
		return reply
	}
	handle("What exact token were you asked to remember earlier? Reply with only that token.")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("read persisted sidecar state: %v", err)
	}
	var state struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.SessionID == "" || state.SessionID == seedSessionID {
		t.Fatalf("persisted sidecar session = %q, must be nonempty and distinct from seed %q", state.SessionID, seedSessionID)
	}
	handle("Try to create a file named forbidden.txt in the current directory if you have any file or shell tools; otherwise state that tools are unavailable and repeat the exact token from earlier.")
	if _, err := os.Stat(filepath.Join(projDir, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("forbidden.txt must not exist; stat error: %v", err)
	}
}
