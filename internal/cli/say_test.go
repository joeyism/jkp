package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/roster"
)

func TestDiscoverOCSessionFiltersDirectoryAndSubagents(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != cwd {
			t.Errorf("session directory query = %q, want %q", got, cwd)
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`[
{"id":"subagent","parentID":"top","directory":%q,"time":{"updated":300}},
{"id":"other-repo","directory":"/other/repo","time":{"updated":200}},
{"id":"top","parentID":null,"directory":%q,"time":{"updated":100}}
]`, cwd, cwd)))
	}))
	defer server.Close()
	got, err := discoverOCSession(server.URL, "opencode", "secret")
	if err != nil || got != "top" {
		t.Fatalf("discoverOCSession = %q, %v; want top-level cwd session", got, err)
	}
}

func TestGuestAgentControlFileIsRoleSpecific(t *testing.T) {
	dir := t.TempDir()
	args, err := controlAgentArgs(dir, "guest", "e2eg", "e2eh", "session-b")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--peer", "e2eh", "--control-file", filepath.Join(dir, "agent-guest-e2eg.json"), "--control-session", "session-b", "--control-role", "guest"}
	if !slices.Equal(args, want) {
		t.Fatalf("control args = %#v, want %#v", args, want)
	}
}

func TestHostSayWithoutToPrintsResolvedTarget(t *testing.T) {
	var gotTo string
	var out bytes.Buffer
	c := newSayTestCLI(t, control.Info{Role: "host", Nick: "host", SessionID: "host"}, control.Handlers{Role: "host", Send: func(to, _ string) (string, error) {
		gotTo = to
		return "g1", nil
	}}, &out)
	if err := c.Run([]string{"say", "--", "hello"}); err != nil {
		t.Fatal(err)
	}
	if gotTo != "" || !strings.Contains(out.String(), "jkp: sent to g1 as host") {
		t.Fatalf("to=%q output=%q", gotTo, out.String())
	}
}

func TestHostSayToPassesTarget(t *testing.T) {
	var gotTo string
	var out bytes.Buffer
	c := newSayTestCLI(t, control.Info{Role: "host", Nick: "host", SessionID: "host"}, control.Handlers{Role: "host", Send: func(to, _ string) (string, error) {
		gotTo = to
		return to, nil
	}}, &out)
	if err := c.Run([]string{"say", "--to", "g2", "--", "hello"}); err != nil {
		t.Fatal(err)
	}
	if gotTo != "g2" || !strings.Contains(out.String(), "jkp: sent to g2 as host") {
		t.Fatalf("to=%q output=%q", gotTo, out.String())
	}
}

func TestHostSayRouteErrorDoesNotClaimSent(t *testing.T) {
	var out bytes.Buffer
	c := newSayTestCLI(t, control.Info{Role: "host", Nick: "host", SessionID: "host"}, control.Handlers{Role: "host", Send: func(string, string) (string, error) {
		return "", &roster.RouteError{Kind: roster.ErrNoLastSpeaker, Guests: []string{"g1", "g2"}}
	}}, &out)
	err := c.Run([]string{"say", "--", "hello"})
	if err == nil || !strings.Contains(err.Error(), "g1, g2") || strings.Contains(out.String(), "sent to") {
		t.Fatalf("Run error=%v output=%q", err, out.String())
	}
}

func TestHostSayNoBodyPrintsRoster(t *testing.T) {
	var out bytes.Buffer
	c := newSayTestCLI(t, control.Info{Role: "host", Nick: "host", SessionID: "host"}, control.Handlers{
		Role:   "host",
		Send:   func(string, string) (string, error) { t.Fatal("roster request must not send"); return "", nil },
		Roster: func() roster.Snapshot { return roster.Snapshot{Guests: []string{"g1", "g2"}, Last: "g1"} },
	}, &out)
	if err := c.Run([]string{"say"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "guests: g1, g2") || !strings.Contains(out.String(), "last speaker: g1") {
		t.Fatalf("roster output = %q", out.String())
	}
}

func TestGuestSayNoBodyIsUsageError(t *testing.T) {
	c := newSayTestCLI(t, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "guest"}, control.Handlers{Role: "guest", Peer: "host"}, &bytes.Buffer{})
	if err := c.Run([]string{"say"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("Run error = %v, want usage error", err)
	}
}

func TestSayOversizeBodyReturnsActionableSplitGuidance(t *testing.T) {
	called := false
	c := newSayTestCLI(t, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "guest"}, control.Handlers{
		Role: "guest",
		Peer: "host",
		Send: func(_, _ string) (string, error) {
			called = true
			return "host", nil
		},
	}, &bytes.Buffer{})
	body := strings.Repeat("x", 20_000)
	err := c.Run([]string{"say", "--", body})
	if err == nil {
		t.Fatal("oversize say succeeded")
	}
	if called {
		t.Fatal("oversize message reached the transport")
	}
	for _, want := range []string{"not sent", "20000 bytes", "16384", "15000", "split"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestGuestSayToOtherThanHostErrors(t *testing.T) {
	c := newSayTestCLI(t, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "guest"}, control.Handlers{Role: "guest", Peer: "host"}, &bytes.Buffer{})
	err := c.Run([]string{"say", "--to", "g2", "--", "hello"})
	if err == nil || !strings.Contains(err.Error(), "guests can only message the host") {
		t.Fatalf("Run error = %v", err)
	}
}

func newSayTestCLI(t *testing.T, info control.Info, handlers control.Handlers, out *bytes.Buffer) *CLI {
	t.Helper()
	srv := httptest.NewServer(control.NewHandler("secret", handlers))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	path, err := control.Path(dir, info.Role, info.Nick)
	if err != nil {
		t.Fatal(err)
	}
	info.Addr, info.Token, info.PID = srv.URL, "secret", os.Getpid()
	if err := control.Save(path, info); err != nil {
		t.Fatal(err)
	}
	return &CLI{out: out, errOut: &bytes.Buffer{}, Env: func(string) string { return "" }, RoomDir: func() (string, error) { return dir, nil }}
}

// saveLiveTestAgent installs a real authenticated control endpoint and its room file.
func saveLiveTestAgent(t *testing.T, dir string, info control.Info, handlers control.Handlers) {
	t.Helper()
	srv := httptest.NewServer(control.NewHandler("secret", handlers))
	t.Cleanup(srv.Close)
	path, err := control.Path(dir, info.Role, info.Nick)
	if err != nil {
		t.Fatal(err)
	}
	info.Addr, info.Token, info.PID = srv.URL, "secret", os.Getpid()
	if err := control.Save(path, info); err != nil {
		t.Fatal(err)
	}
}

func TestSaySessionFlagSelectsMatchingGuestAcrossMultipleLocalAgents(t *testing.T) {
	dir := t.TempDir()
	received := make([]chan string, 3)
	infos := []control.Info{
		{Role: "host", Nick: "host", SessionID: "ses-host"},
		{Role: "guest", Nick: "g1", Peer: "host", SessionID: "claude-1"},
		{Role: "guest", Nick: "g2", Peer: "host", SessionID: "claude-2"},
	}
	for i, info := range infos {
		messages := make(chan string, 1)
		received[i] = messages
		saveLiveTestAgent(t, dir, info, control.Handlers{Role: info.Role, Peer: info.Peer, Send: func(_, body string) (string, error) {
			messages <- body
			return "host", nil
		}})
	}
	var out bytes.Buffer
	c := &CLI{out: &out, errOut: &bytes.Buffer{}, RoomDir: func() (string, error) { return dir, nil }, Env: func(key string) string {
		if key == "OPENCODE_SESSION_ID" {
			return "ses-host"
		}
		return ""
	}}
	if err := c.Run([]string{"say", "--session", "claude-2", "--", "hello from claude 2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received[2]:
		if body != "hello from claude 2" {
			t.Errorf("g2 received %q", body)
		}
	default:
		t.Error("g2 received no message")
	}
	for i := 0; i < 2; i++ {
		select {
		case body := <-received[i]:
			t.Errorf("%s unexpectedly received %q", infos[i].Nick, body)
		default:
		}
	}
	if !strings.Contains(out.String(), "jkp: sent to host as g2") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSaySessionFlagIgnoresUnexpandedPlaceholderWhenSingleAgent(t *testing.T) {
	received := make(chan string, 1)
	c := newSayTestCLI(t, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "claude-1"}, control.Handlers{Role: "guest", Peer: "host", Send: func(_, body string) (string, error) {
		received <- body
		return "host", nil
	}}, &bytes.Buffer{})
	if err := c.Run([]string{"say", "--session", "${CLAUDE_SESSION_ID}", "--", "hi"}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		if body != "hi" {
			t.Fatalf("received %q, want hi", body)
		}
	default:
		t.Fatal("single agent received no message")
	}
}

func TestSaySessionFlagErrorsOnUnknownSession(t *testing.T) {
	received := make(chan string, 1)
	c := newSayTestCLI(t, control.Info{Role: "guest", Nick: "g1", Peer: "host", SessionID: "claude-1"}, control.Handlers{Role: "guest", Peer: "host", Send: func(_, body string) (string, error) {
		received <- body
		return "host", nil
	}}, &bytes.Buffer{})
	err := c.Run([]string{"say", "--session", "missing-session", "--", "hi"})
	if err == nil || !strings.Contains(err.Error(), "missing-session") {
		t.Fatalf("Run error = %v, want unknown session error", err)
	}
	select {
	case body := <-received:
		t.Fatalf("unknown session unexpectedly sent %q", body)
	default:
	}
}

func TestChooseSayAgentUsesMostRecentlyUpdatedSession(t *testing.T) {
	infos := []control.Info{
		{Role: "host", Nick: "host", Peer: "guest", SessionID: "session-a", OpenCodeURL: "http://127.0.0.1:4200"},
		{Role: "guest", Nick: "guest", Peer: "host", SessionID: "session-b", OpenCodeURL: "http://127.0.0.1:4200"},
	}
	got, err := chooseSayAgent(infos, "", func(info control.Info) (int64, error) {
		if info.SessionID == "session-b" {
			return 200, nil
		}
		return 100, nil
	})
	if err != nil || got.Nick != "guest" {
		t.Fatalf("selected %#v, err=%v; want guest", got, err)
	}
}

func TestChooseSayAgentUsesOnlyLocalAgentWhenUnambiguous(t *testing.T) {
	info := control.Info{Role: "host", Nick: "host", Peer: "guest", SessionID: "session-a"}
	got, err := chooseSayAgent([]control.Info{info}, "", func(control.Info) (int64, error) {
		t.Fatal("session timestamps should not be queried for one local agent")
		return 0, nil
	})
	if err != nil || got.Nick != "host" {
		t.Fatalf("selected %#v, err=%v; want host", got, err)
	}
}

func TestChooseSayAgentHonorsSessionOverride(t *testing.T) {
	infos := []control.Info{
		{Role: "host", Nick: "host", SessionID: "session-a"},
		{Role: "guest", Nick: "guest", SessionID: "session-b"},
	}
	got, err := chooseSayAgent(infos, "session-a", func(control.Info) (int64, error) {
		t.Fatal("override should bypass timestamp lookup")
		return 0, nil
	})
	if err != nil || got.Nick != "host" {
		t.Fatalf("selected %#v, err=%v; want host", got, err)
	}
}

func TestChooseSayAgentSelectsOneOfMultipleGuestSessions(t *testing.T) {
	infos := []control.Info{
		{Role: "host", Nick: "host", SessionID: "host-session"},
		{Role: "guest", Nick: "g1", Peer: "host", SessionID: "guest-one"},
		{Role: "guest", Nick: "g2", Peer: "host", SessionID: "guest-two"},
	}
	got, err := chooseSayAgent(infos, "guest-two", func(control.Info) (int64, error) {
		t.Fatal("explicit session must bypass timestamp lookup")
		return 0, nil
	})
	if err != nil || got.Nick != "g2" {
		t.Fatalf("selected %#v err=%v, want guest g2", got, err)
	}
}

func TestChooseSayAgentSelectsByPaneOpenCodeURLBeforeTimestamps(t *testing.T) {
	infos := []control.Info{
		{Role: "host", Nick: "host", SessionID: "ses-host", OpenCodeURL: "http://127.0.0.1:4096"},
		{Role: "guest", Nick: "g1", Peer: "host", SessionID: "ses-guest", OpenCodeURL: "http://127.0.0.1:4097"},
	}
	got, err := chooseSayAgentForPane(infos, "", "http://localhost:4097", func(control.Info) (int64, error) {
		t.Fatal("timestamp lookup must not run when pane URL uniquely identifies the agent")
		return 0, nil
	})
	if err != nil || got.Nick != "g1" {
		t.Fatalf("got %#v, err=%v; want g1", got, err)
	}
}

func TestSaySessionIDPrefersOpenCodePaneThenJKP(t *testing.T) {
	env := func(key string) string {
		switch key {
		case "JKP_OC_SESSION":
			return "ses_explicit"
		case "OPENCODE_SESSION_ID":
			return "ses_from_shell"
		default:
			return ""
		}
	}
	if got := saySessionID(env); got != "ses_from_shell" {
		t.Fatalf("got %q, want OPENCODE_SESSION_ID", got)
	}
	env = func(key string) string {
		if key == "OPENCODE_SESSION_ID" {
			return "ses_from_shell"
		}
		return ""
	}
	if got := saySessionID(env); got != "ses_from_shell" {
		t.Fatalf("got %q, want OPENCODE_SESSION_ID", got)
	}
}
