package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/control"
)

func newLogTestCLI(dir string, out *bytes.Buffer) *CLI {
	return &CLI{out: out, errOut: &bytes.Buffer{}, Env: func(string) string { return "" }, RoomDir: func() (string, error) { return dir, nil }}
}

func TestLogPrintsTailForSelectedSession(t *testing.T) {
	dir := t.TempDir()
	for _, nick := range []string{"g1", "g2"} {
		logFile := filepath.Join(dir, nick+".log")
		body := fmt.Sprintf("jkp: <- host: question for %s\njkp: -> host: answer from %s\n", nick, nick)
		if err := os.WriteFile(logFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		session := "claude-1"
		if nick == "g2" {
			session = "claude-2"
		}
		saveLiveTestAgent(t, dir, control.Info{Role: "guest", Nick: nick, Peer: "host", SessionID: session, LogFile: logFile}, control.Handlers{Role: "guest", Peer: "host"})
	}
	var out bytes.Buffer
	c := newLogTestCLI(dir, &out)
	if err := c.Run([]string{"log", "--session", "claude-2"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"question for g2", "answer from g2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q: %q", want, out.String())
		}
	}
	for _, unwanted := range []string{"question for g1", "answer from g1"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("output contains unselected log %q: %q", unwanted, out.String())
		}
	}
}

func TestLogRespectsLinesFlag(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "g1.log")
	var content strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&content, "line %02d\n", i)
	}
	if err := os.WriteFile(logFile, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	saveLiveTestAgent(t, dir, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "claude-1", LogFile: logFile}, control.Handlers{Role: "guest", Peer: "host"})
	var out bytes.Buffer
	c := newLogTestCLI(dir, &out)
	if err := c.Run([]string{"log", "--lines", "5"}); err != nil {
		t.Fatal(err)
	}
	for i := 56; i <= 60; i++ {
		if !strings.Contains(out.String(), fmt.Sprintf("line %02d\n", i)) {
			t.Errorf("output missing line %d: %q", i, out.String())
		}
	}
	if strings.Contains(out.String(), "line 55\n") {
		t.Fatalf("output includes line before requested tail: %q", out.String())
	}
}

func TestLogErrorsWhenNotInARoom(t *testing.T) {
	c := newLogTestCLI(t.TempDir(), &bytes.Buffer{})
	err := c.Run([]string{"log"})
	if err == nil || !strings.Contains(err.Error(), "not in a room") {
		t.Fatalf("Run error = %v, want not-in-room error", err)
	}
}

func TestLogHandlesEmptyLogFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "g1.log")
	if err := os.WriteFile(logFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	saveLiveTestAgent(t, dir, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "claude-1", LogFile: logFile}, control.Handlers{Role: "guest", Peer: "host"})
	var out bytes.Buffer
	c := newLogTestCLI(dir, &out)
	if err := c.Run([]string{"log"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no log output yet") {
		t.Fatalf("empty log output = %q", out.String())
	}
}
