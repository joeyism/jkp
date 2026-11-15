package host

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/room"
)

func TestDiscoverOpenCodeHonorsExplicitBinding(t *testing.T) {
	cwd := t.TempDir()
	server := newOpenCodeTestServer(t, "/path", fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd), fmt.Sprintf(`[{"id":"ses_selected","directory":%q,"time":{"updated":10}}]`, cwd))
	values := map[string]string{
		"JKP_OC_URL":     server.URL,
		"JKP_OC_SESSION": "ses_selected",
	}
	url, session, err := discoverOpenCode(func(key string) string { return values[key] }, t.TempDir(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if url != values["JKP_OC_URL"] || session != values["JKP_OC_SESSION"] {
		t.Fatalf("binding = (%q, %q), want (%q, %q)", url, session, values["JKP_OC_URL"], values["JKP_OC_SESSION"])
	}
}

func TestPickNewestUnboundOpenCodeSession(t *testing.T) {
	sessions := []ocSession{
		{ID: "host-session", Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 20}},
		{ID: "guest-session", Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 30}},
	}
	got := pickUnboundSession(sessions, map[string]bool{"host-session": true}, "")
	if got != "guest-session" {
		t.Fatalf("selected session %q, want guest-session", got)
	}
}

func TestPickUnboundOpenCodeSessionReturnsEmptyWhenAllTaken(t *testing.T) {
	sessions := []ocSession{{ID: "only-session"}}
	got := pickUnboundSession(sessions, map[string]bool{"only-session": true}, "")
	if got != "" {
		t.Fatalf("selected session %q, want none", got)
	}
}

func TestPickUnboundOpenCodeSessionHonorsBindingFromSiblingPort(t *testing.T) {
	sessions := []ocSession{
		{ID: "ses_host", Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 500}},
		{ID: "ses_guest", Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 100}},
	}
	got := pickUnboundSession(sessions, map[string]bool{"ses_host": true}, "")
	if got != "ses_guest" {
		t.Fatalf("selected %q, want ses_guest; ses_host is bound on another local port", got)
	}
}

func TestReusableHostBindingRejectsDifferentServerPortOnSameMachine(t *testing.T) {
	cwd := t.TempDir()
	server := newOpenCodeTestServer(t, "/path",
		fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd),
		fmt.Sprintf(`[{"id":"ses-host","directory":%q}]`, cwd))
	err := validateReusableHostBindingForPane(
		control.Info{OpenCodeURL: server.URL, SessionID: "ses-host"},
		"http://127.0.0.1:4097", "", cwd, "",
	)
	if err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("expected port mismatch conflict, got %v", err)
	}
}

func TestDiscoverOpenCodeSkipsSessionBoundByLiveLocalAgent(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	server := newOpenCodeTestServer(t, "/path", fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd), fmt.Sprintf(`[{"id":"host-session","directory":%q,"time":{"updated":20}},{"id":"guest-session","directory":%q,"time":{"updated":30}}]`, cwd, cwd))
	path, err := control.Path(dir, "host")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(path, control.Info{
		Addr: "http://127.0.0.1:1", Token: "secret", Role: "host", Nick: "host", Peer: "guest", PID: os.Getpid(),
		SessionID: "host-session", OpenCodeURL: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JKP_OC_URL": server.URL}
	url, session, err := discoverOpenCode(func(key string) string { return values[key] }, dir, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if url != server.URL || session != "guest-session" {
		t.Fatalf("binding = (%q, %q), want (%q, guest-session)", url, session, server.URL)
	}
	if _, err := os.Stat(filepath.Join(dir, control.HostFileName)); err != nil {
		t.Fatal(err)
	}
}

func TestBoundOpenCodeSessionsTracksSessionAcrossServerPorts(t *testing.T) {
	dir := t.TempDir()
	path, err := control.Path(dir, "host")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(path, control.Info{
		Addr: "http://127.0.0.1:1", Token: "secret", Role: "host", Nick: "host",
		PID: os.Getpid(), SessionID: "ses_host", OpenCodeURL: "http://127.0.0.1:4096",
	}); err != nil {
		t.Fatal(err)
	}
	bound, err := boundOpenCodeSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bound["ses_host"] {
		t.Fatalf("session bound on :4096 not recorded globally: %#v", bound)
	}
	if got := pickUnboundSession([]ocSession{{ID: "ses_host", Time: struct {
		Updated int64 `json:"updated"`
	}{Updated: 500}}, {ID: "ses_guest", Time: struct {
		Updated int64 `json:"updated"`
	}{Updated: 100}}}, bound, ""); got != "ses_guest" {
		t.Fatalf("selected %q, want ses_guest when host session is bound on another port", got)
	}
}

func TestExistingHostReusesItsCurrentOpenCodeBinding(t *testing.T) {
	dir := t.TempDir()
	_, err := room.Create(dir, room.Material{HostPassword: "host", JoinPassword: "guest", FP: "fp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := room.SavePID(dir, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	path, err := control.Path(dir, "host")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(path, control.Info{
		Addr: "http://127.0.0.1:1", Token: "secret", Role: "host", Nick: "host", Peer: "guest", PID: os.Getpid(),
		SessionID: "host-session", OpenCodeURL: "http://127.0.0.1:4200",
	}); err != nil {
		t.Fatal(err)
	}
	url, session, ok := reusableHostBinding(dir)
	if !ok || url != "http://127.0.0.1:4200" || session != "host-session" {
		t.Fatalf("reused binding = (%q, %q, %v)", url, session, ok)
	}
}

func TestDiscoverOpenCodeLegacyLiveAgentTellsUserToReset(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"addr":"http://127.0.0.1:1234","token":"secret","nick":"host","peer":"guest","pid":` + strconv.Itoa(os.Getpid()) + `}`
	if err := os.WriteFile(filepath.Join(dir, control.LegacyFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := DiscoverOpenCodeIn(func(string) string { return "" }, dir)
	if err == nil || !strings.Contains(err.Error(), "jkp reset") {
		t.Fatalf("got %v, want reset guidance for leftover shared agent", err)
	}
}

func TestPickUnboundSessionPrefersExactWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	sessions := []ocSession{
		{ID: "other-newer", Directory: other, Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 90}},
		{ID: "cwd-older", Directory: dir, Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 10}},
	}
	got := pickUnboundSession(sessions, map[string]bool{}, dir)
	if got != "cwd-older" {
		t.Fatalf("selected %q, want the session in cwd even if older", got)
	}
}

func TestPickUnboundSessionNeverFallsBackToAnotherDirectory(t *testing.T) {
	cwd := t.TempDir()
	other := t.TempDir()
	sessions := []ocSession{{
		ID: "delorean-session", Directory: other,
		Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 100},
	}}
	got := pickUnboundSession(sessions, map[string]bool{}, cwd)
	if got != "" {
		t.Fatalf("selected cross-directory session %q; want no session", got)
	}
}

func TestPickUnboundSessionSkipsSubagentSessions(t *testing.T) {
	cwd := t.TempDir()
	sessions := []ocSession{
		{ID: "subagent", ParentID: "top-level", Directory: cwd, Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 200}},
		{ID: "top-level", Directory: cwd, Time: struct {
			Updated int64 `json:"updated"`
		}{Updated: 100}},
	}
	if got := pickUnboundSession(sessions, map[string]bool{}, cwd); got != "top-level" {
		t.Fatalf("selected %q, want top-level session", got)
	}
}

func TestFetchSessionsPassesDirectoryQueryParam(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != canonicalPath(cwd) {
			t.Errorf("directory query = %q, want %q", got, canonicalPath(cwd))
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	if _, ok := fetchSessions(server.URL, "", cwd); !ok {
		t.Fatal("fetchSessions failed")
	}
}

func TestOpenCodeSessionUpdatedReadsSessionEndpointDirectly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses-target" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"ses-target","time":{"updated":12345}}`))
	}))
	defer server.Close()
	if got, ok := OpenCodeSessionUpdated(server.URL, "", "ses-target"); !ok || got != 12345 {
		t.Fatalf("OpenCodeSessionUpdated = (%d,%t), want (12345,true)", got, ok)
	}
}

func TestFetchSessionsPassesCanonicalDirectoryToServer(t *testing.T) {
	parent := t.TempDir()
	link := filepath.Join(parent, "linked")
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	want := canonicalPath(link)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("directory"); got != want {
			t.Errorf("directory query = %q, want canonical %q", got, want)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	if _, ok := fetchSessions(server.URL, "", link); !ok {
		t.Fatal("fetchSessions failed")
	}
}

func TestPortScanSkipsServerServingDifferentDirectory(t *testing.T) {
	server := newOpenCodeTestServer(t, `/path`, `{"directory":"/other/repo","worktree":"/other/repo"}`, `[ {"id":"wrong","directory":"/other/repo","time":{"updated":99}} ]`)
	cwd := t.TempDir()
	_, _, err := discoverOpenCodeWithCandidates(func(string) string { return "" }, t.TempDir(), cwd, []string{server.URL})
	if err == nil || !strings.Contains(err.Error(), "opencode --port 4096") {
		t.Fatalf("discover error = %v, want served-directory error with vanilla OpenCode instructions", err)
	}
}

func TestPortScanSelectsServerWhoseWorktreeContainsCwd(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "sub")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	other := newOpenCodeTestServer(t, `/path`, `{"directory":"/elsewhere","worktree":"/elsewhere"}`, `[]`)
	match := newOpenCodeTestServer(t, `/path`, fmt.Sprintf(`{"directory":%q,"worktree":%q}`, parent, parent), fmt.Sprintf(`[{"id":"cwd-session","directory":%q,"time":{"updated":10}}]`, cwd))
	url, session, err := discoverOpenCodeWithCandidates(func(string) string { return "" }, t.TempDir(), cwd, []string{other.URL, match.URL})
	if err != nil || url != match.URL || session != "cwd-session" {
		t.Fatalf("binding=(%q,%q), err=%v; want matching worktree and cwd session", url, session, err)
	}
}

func TestDeadOcmuxRegistryEntryFallsBackToMatchingCandidate(t *testing.T) {
	cwd := t.TempDir()
	registry := filepath.Join(t.TempDir(), "serve-processes.json")
	if err := os.WriteFile(registry, []byte(fmt.Sprintf(`[{"port":1,"cwd":%q}]`, cwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newOpenCodeTestServer(t, `/path`, fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd), fmt.Sprintf(`[{"id":"jkp-session","directory":%q,"time":{"updated":10}}]`, cwd))
	values := map[string]string{"JKP_OCMUX_REGISTRY": registry}
	url, session, err := discoverOpenCodeWithCandidates(func(key string) string { return values[key] }, t.TempDir(), cwd, []string{server.URL})
	if err != nil || url != server.URL || session != "jkp-session" {
		t.Fatalf("binding=(%q,%q), err=%v; want healthy candidate after dead registry port", url, session, err)
	}
}

func TestExplicitSessionIDComesFromOpenCodeProcessCommand(t *testing.T) {
	for _, command := range []string{
		"opencode -s ses_selected /repo",
		"/usr/local/bin/opencode --session ses_selected",
		"opencode --session=ses_selected",
	} {
		if got := sessionIDFromCommandLine(command); got != "ses_selected" {
			t.Errorf("sessionIDFromCommandLine(%q) = %q", command, got)
		}
	}
	if got := sessionIDFromCommandLine("opencode --port 4096"); got != "" {
		t.Fatalf("sessionIDFromCommandLine without a session = %q", got)
	}
}

func TestParseOpenCodeCommandLineExtractsPaneBinding(t *testing.T) {
	for _, tc := range []struct {
		name        string
		command     string
		sessionID   string
		serverURL   string
		unservedTUI bool
	}{
		{
			name:      "tui with session and port",
			command:   "opencode -s ses_guest --port 4097",
			sessionID: "ses_guest",
			serverURL: "http://127.0.0.1:4097",
		},
		{
			name:      "equals flags and wildcard hostname",
			command:   "/opt/homebrew/bin/opencode --session=ses_a --hostname=0.0.0.0 --port=4102 /repo",
			sessionID: "ses_a",
			serverURL: "http://127.0.0.1:4102",
		},
		{
			name:      "attach command",
			command:   "opencode attach http://localhost:4200 --session ses_attached",
			sessionID: "ses_attached",
			serverURL: "http://127.0.0.1:4200",
		},
		{
			name:      "serve subcommand",
			command:   "opencode serve --port 4203",
			serverURL: "http://127.0.0.1:4203",
		},
		{
			name:        "plain unserved tui with session",
			command:     "opencode -s ses_plain",
			sessionID:   "ses_plain",
			unservedTUI: true,
		},
		{
			name:        "bare unserved tui",
			command:     "opencode",
			unservedTUI: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOpenCodeCommandLine(tc.command)
			if got.SessionID != tc.sessionID || got.ServerURL != tc.serverURL || got.UnservedTUI != tc.unservedTUI {
				t.Fatalf("parseOpenCodeCommandLine(%q) = %+v; want session=%q URL=%q unserved=%t",
					tc.command, got, tc.sessionID, tc.serverURL, tc.unservedTUI)
			}
		})
	}
}

func TestPaneOpenCodeBindingHonorsExplicitOverridesAndProcessCommand(t *testing.T) {
	values := map[string]string{"OPENCODE_PID": "123", "OPENCODE_SESSION_ID": "ses_env"}
	lookup := func(pid string) string {
		if pid == "123" {
			return "opencode -s ses_process --port 4097"
		}
		return ""
	}
	getenv := func(key string) string { return values[key] }
	serverURL, sessionID, unserved := PaneOpenCodeBindingWith(getenv, lookup)
	if serverURL != "http://127.0.0.1:4097" || sessionID != "ses_env" || unserved {
		t.Fatalf("pane binding = (%q, %q, %t), want process URL and environment session", serverURL, sessionID, unserved)
	}
	values["JKP_OC_URL"] = "http://localhost:4200/"
	values["JKP_OC_SESSION"] = "ses_override"
	serverURL, sessionID, unserved = PaneOpenCodeBindingWith(getenv, lookup)
	if serverURL != "http://127.0.0.1:4200" || sessionID != "ses_override" || unserved {
		t.Fatalf("explicit pane binding = (%q, %q, %t)", serverURL, sessionID, unserved)
	}
}

func TestDefaultOpenCodeCandidatesIncludeSecondPanePorts(t *testing.T) {
	want := map[string]bool{
		"http://127.0.0.1:4096": false,
		"http://127.0.0.1:4097": false,
		"http://127.0.0.1:4110": false,
		"http://127.0.0.1:4200": false,
	}
	for _, candidate := range defaultOpenCodeCandidates() {
		if _, ok := want[candidate]; ok {
			want[candidate] = true
		}
	}
	for candidate, found := range want {
		if !found {
			t.Errorf("fallback candidates missing %s", candidate)
		}
	}
}

func TestDiscoverOpenCodeUsesPanePortBeforeSiblingCandidate(t *testing.T) {
	cwd := t.TempDir()
	sessions := fmt.Sprintf(`[{"id":"ses_host","directory":%q,"time":{"updated":500}},{"id":"ses_guest","directory":%q,"time":{"updated":100}}]`, cwd, cwd)
	pathJSON := fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd)
	hostServer := newOpenCodeTestServer(t, "/path", pathJSON, sessions)
	guestServer := newOpenCodeTestServer(t, "/path", pathJSON, sessions)
	guestPort := guestServer.Listener.Addr().(*net.TCPAddr).Port
	getenv := func(key string) string {
		if key == "OPENCODE_PID" {
			return "6112"
		}
		return ""
	}
	lookup := func(pid string) string {
		if pid == "6112" {
			return fmt.Sprintf("opencode -s ses_guest --port %d", guestPort)
		}
		return ""
	}
	gotURL, gotSession, err := discoverOpenCodeWithProcess(getenv, lookup, t.TempDir(), cwd, []string{hostServer.URL})
	if err != nil {
		t.Fatalf("discoverOpenCodeWithProcess: %v", err)
	}
	if gotURL != guestServer.URL || gotSession != "ses_guest" {
		t.Fatalf("binding = (%q, %q), want (%q, ses_guest); sibling was %q", gotURL, gotSession, guestServer.URL, hostServer.URL)
	}
}

func TestDiscoverOpenCodeRejectsUnservedTUIInsteadOfUsingSibling(t *testing.T) {
	cwd := t.TempDir()
	sibling := newOpenCodeTestServer(t, "/path",
		fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd),
		fmt.Sprintf(`[{"id":"ses_plain","directory":%q,"time":{"updated":100}}]`, cwd))
	getenv := func(key string) string {
		if key == "OPENCODE_PID" {
			return "33369"
		}
		return ""
	}
	lookup := func(pid string) string {
		if pid == "33369" {
			return "opencode -s ses_plain"
		}
		return ""
	}
	_, _, err := discoverOpenCodeWithProcess(getenv, lookup, t.TempDir(), cwd, []string{sibling.URL})
	if err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("expected unserved-TUI guidance, got %v", err)
	}
}

func TestReusableHostBindingRejectsDifferentDirectory(t *testing.T) {
	other := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/path":
			_, _ = fmt.Fprintf(w, `{"directory":%q,"worktree":%q}`, other, other)
		case "/session/ses-delorean":
			_, _ = fmt.Fprintf(w, `{"id":"ses-delorean","directory":%q}`, other)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cwd := t.TempDir()
	err := validateReusableHostBinding(control.Info{OpenCodeURL: server.URL, SessionID: "ses-delorean"}, "", cwd, "")
	if err == nil || !strings.Contains(err.Error(), "already bound") || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "jkp reset") {
		t.Fatalf("binding error = %v, want cross-directory host reset guidance", err)
	}
}

func TestReusableHostBindingAllowsSameDirectory(t *testing.T) {
	cwd := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/path":
			_, _ = fmt.Fprintf(w, `{"directory":%q,"worktree":%q}`, cwd, cwd)
		case "/session/ses-jkp":
			_, _ = fmt.Fprintf(w, `{"id":"ses-jkp","directory":%q}`, cwd)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := validateReusableHostBinding(control.Info{OpenCodeURL: server.URL, SessionID: "ses-jkp"}, "ses-jkp", cwd, ""); err != nil {
		t.Fatalf("same-directory binding rejected: %v", err)
	}
}

func TestReusableHostBindingRejectsExplicitSessionMismatch(t *testing.T) {
	cwd := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/path" {
			_, _ = fmt.Fprintf(w, `{"directory":%q,"worktree":%q}`, cwd, cwd)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	err := validateReusableHostBinding(control.Info{OpenCodeURL: server.URL, SessionID: "ses-running"}, "ses-requested", cwd, "")
	if err == nil || !strings.Contains(err.Error(), "already bound") || !strings.Contains(err.Error(), "ses-running") {
		t.Fatalf("binding error = %v, want explicit session conflict", err)
	}
}

func TestRealDepsRejectsLiveHostBindingFromAnotherDirectory(t *testing.T) {
	other, cwd := t.TempDir(), t.TempDir()
	restore := chdirForTest(t, cwd)
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/path":
			_, _ = fmt.Fprintf(w, `{"directory":%q,"worktree":%q}`, other, other)
		case "/session/ses-old":
			_, _ = fmt.Fprintf(w, `{"id":"ses-old","directory":%q}`, other)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	writeLiveHostRoom(t, cwd, server.URL, "ses-old")
	t.Setenv("OPENCODE_PID", "")
	t.Setenv("OPENCODE_SESSION_ID", "")
	t.Setenv("JKP_OC_SESSION", "")
	err := func() error {
		_, err := RealDeps(cwd, "", "")
		return err
	}()
	if err == nil || !strings.Contains(err.Error(), "already bound") || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "jkp reset") {
		t.Fatalf("RealDeps error = %v, want cross-directory host warning", err)
	}
}

func TestRealDepsReusesHostBindingInCurrentDirectory(t *testing.T) {
	cwd := t.TempDir()
	restore := chdirForTest(t, cwd)
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/path":
			_, _ = fmt.Fprintf(w, `{"directory":%q,"worktree":%q}`, cwd, cwd)
		case "/session/ses-current":
			_, _ = fmt.Fprintf(w, `{"id":"ses-current","directory":%q}`, cwd)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	writeLiveHostRoom(t, cwd, server.URL, "ses-current")
	t.Setenv("OPENCODE_PID", "")
	t.Setenv("OPENCODE_SESSION_ID", "")
	t.Setenv("JKP_OC_SESSION", "")
	deps, err := RealDeps(cwd, "", "")
	if err != nil || deps.OpenCodeURL != server.URL || deps.OpenCodeSession != "ses-current" {
		t.Fatalf("RealDeps binding=(%q,%q), err=%v", deps.OpenCodeURL, deps.OpenCodeSession, err)
	}
}

func TestRealDepsRejectsReusableHostBindingFromAnotherOpenCodePort(t *testing.T) {
	cwd := t.TempDir()
	restore := chdirForTest(t, cwd)
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/path":
			_, _ = fmt.Fprintf(w, `{"directory":%q,"worktree":%q}`, cwd, cwd)
		case "/session/ses-current":
			_, _ = fmt.Fprintf(w, `{"id":"ses-current","directory":%q}`, cwd)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	writeLiveHostRoom(t, cwd, server.URL, "ses-current")
	t.Setenv("OPENCODE_PID", "")
	t.Setenv("OPENCODE_SESSION_ID", "")
	t.Setenv("JKP_OC_SESSION", "")
	t.Setenv("JKP_OC_URL", "http://127.0.0.1:4097")
	_, err := RealDeps(cwd, "", "")
	if err == nil || !strings.Contains(err.Error(), "already bound") || !strings.Contains(err.Error(), "4097") {
		t.Fatalf("RealDeps error = %v, want different-pane server conflict", err)
	}
}

func chdirForTest(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}
}

func writeLiveHostRoom(t *testing.T, dir, serverURL, session string) {
	t.Helper()
	if _, err := room.Create(dir, room.Material{HostPassword: "host", JoinPassword: "join", FP: strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	if err := room.SavePID(dir, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	path, err := control.Path(dir, "host")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(path, control.Info{
		Addr: "http://127.0.0.1:1", Token: "secret", Role: "host", Nick: "host",
		PID: os.Getpid(), SessionID: session, OpenCodeURL: serverURL,
	}); err != nil {
		t.Fatal(err)
	}
}

func newOpenCodeTestServer(t *testing.T, path, pathJSON, sessionsJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == path:
			_, _ = w.Write([]byte(pathJSON))
		case r.URL.Path == "/session":
			_, _ = w.Write([]byte(sessionsJSON))
		case strings.HasPrefix(r.URL.Path, "/session/"):
			var sessions []ocSession
			if err := json.Unmarshal([]byte(sessionsJSON), &sessions); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, "/session/")
			for _, session := range sessions {
				if session.ID == id {
					_ = json.NewEncoder(w).Encode(session)
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestOpenCodeSessionIDEnvBindsThisPane(t *testing.T) {
	cwd := t.TempDir()
	server := newOpenCodeTestServer(t, "/path", fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd), fmt.Sprintf(`[{"id":"ses_from_shell","directory":%q,"time":{"updated":10}}]`, cwd))
	values := map[string]string{
		"JKP_OC_URL":          server.URL,
		"OPENCODE_SESSION_ID": "ses_from_shell",
	}
	url, session, err := discoverOpenCode(func(key string) string { return values[key] }, t.TempDir(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if url != values["JKP_OC_URL"] || session != "ses_from_shell" {
		t.Fatalf("binding = (%q, %q)", url, session)
	}
}

func TestJKPSessionOverrideWinsOverOpenCodeSessionID(t *testing.T) {
	cwd := t.TempDir()
	server := newOpenCodeTestServer(t, "/path", fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd), fmt.Sprintf(`[{"id":"ses_explicit","directory":%q,"time":{"updated":10}},{"id":"ses_from_shell","directory":%q,"time":{"updated":20}}]`, cwd, cwd))
	values := map[string]string{
		"JKP_OC_URL":          server.URL,
		"JKP_OC_SESSION":      "ses_explicit",
		"OPENCODE_SESSION_ID": "ses_from_shell",
	}
	_, session, err := discoverOpenCode(func(key string) string { return values[key] }, t.TempDir(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if session != "ses_explicit" {
		t.Fatalf("session = %q, want ses_explicit", session)
	}
}

func TestOcmuxRegistryMapsCwdToServePort(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "serve-processes.json")
	cwd := filepath.Join(dir, "video-dedupe-by-hash")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`[{"port":4200,"cwd":%q},{"port":4202,"cwd":%q}]`, filepath.Join(dir, "other"), cwd)
	if err := os.WriteFile(registry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ocmuxURLForCwd(cwd, func(key string) string {
		if key == "JKP_OCMUX_REGISTRY" {
			return registry
		}
		return ""
	})
	if got != "http://127.0.0.1:4202" {
		t.Fatalf("url = %q, want pane port 4202", got)
	}
}

func TestOcmuxRegistryCanonicalizesSymlinkedCwd(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(actual, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	registry := filepath.Join(root, "serve-processes.json")
	if err := os.WriteFile(registry, []byte(fmt.Sprintf(`[{"port":4202,"cwd":%q}]`, actual)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ocmuxURLForCwd(link, func(key string) string {
		if key == "JKP_OCMUX_REGISTRY" {
			return registry
		}
		return ""
	})
	if got != "http://127.0.0.1:4202" {
		t.Fatalf("url = %q, want symlink-equivalent port 4202", got)
	}
}

func TestDiscoverUsesExplicitSessionOnCurrentServer(t *testing.T) {
	cwd := t.TempDir()
	server := newOpenCodeTestServer(t, "/path", fmt.Sprintf(`{"directory":%q,"worktree":%q}`, cwd, cwd), fmt.Sprintf(`[{"id":"ses_pane","directory":%q,"time":{"updated":10}}]`, cwd))
	values := map[string]string{
		"JKP_OC_URL":          server.URL,
		"OPENCODE_SESSION_ID": "ses_pane",
	}
	url, session, err := discoverOpenCode(func(key string) string { return values[key] }, t.TempDir(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if url != server.URL || session != "ses_pane" {
		t.Fatalf("binding = (%q, %q)", url, session)
	}
}
