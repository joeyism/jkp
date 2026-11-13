package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/roster"
)

func TestSendPostsMessageToAuthenticatedAgent(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(control.NewHandler("secret", control.Handlers{Role: "guest", Peer: "host", Send: func(to, body string) (string, error) {
		if to != "host" {
			t.Errorf("guest target = %q, want host", to)
		}
		gotBody = body
		return "host", nil
	}}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "agent.json")
	if err := control.Save(path, control.Info{Addr: srv.URL, Token: "secret", Role: "guest", Nick: "guest", Peer: "host", PID: 42, SessionID: "ses_guest", OpenCodeURL: "http://127.0.0.1:4200"}); err != nil {
		t.Fatal(err)
	}
	client := control.NewClient(path)
	if target, err := client.Send(context.Background(), "", "Could you inspect this change?"); err != nil || target != "host" {
		t.Fatalf("Send target=%q err=%v", target, err)
	}
	if gotBody != "Could you inspect this change?" {
		t.Fatalf("sent body=%q", gotBody)
	}
}

func TestSendWithoutAgentFileExplainsHowToJoin(t *testing.T) {
	client := control.NewClient(filepath.Join(t.TempDir(), "missing.json"))
	_, err := client.Send(context.Background(), "", "hello")
	if err == nil || !strings.Contains(err.Error(), "not in a room") || !strings.Contains(err.Error(), "jkp help") {
		t.Fatalf("got %v, want not-in-room help error", err)
	}
}

func TestSaveWritesPrivateControlFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := control.Save(path, control.Info{Addr: "http://127.0.0.1:1234", Token: "secret", Role: "host", Nick: "host", Peer: "guest", PID: 7, SessionID: "ses_host"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("control file mode %o, want 0600", fi.Mode().Perm())
	}
	var got control.Info
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &got) != nil || got.PID != 7 || got.Peer != "guest" {
		t.Fatalf("control info = %#v, err=%v", got, err)
	}
}

func TestSaveAndLoadPreservesLogFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-guest-g1.json")
	want := "/tmp/jkp-guest-g1.log"
	if err := control.Save(path, control.Info{Addr: "http://127.0.0.1:1234", Token: "secret", Role: "guest", Nick: "g1", Peer: "host", PID: os.Getpid(), SessionID: "ses_c1", LogFile: want}); err != nil {
		t.Fatal(err)
	}
	info, err := control.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.LogFile != want {
		t.Errorf("Load LogFile = %q, want %q", info.LogFile, want)
	}
	infos, err := control.LoadAgents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("LoadAgents = %#v, want one agent", infos)
	}
	if infos[0].LogFile != want {
		t.Errorf("LoadAgents LogFile = %q, want %q", infos[0].LogFile, want)
	}
}

func TestSaveHostWithoutPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), control.HostFileName)
	if err := control.Save(path, control.Info{Addr: "http://127.0.0.1:1234", Token: "secret", Role: "host", Nick: "host", PID: os.Getpid(), SessionID: "ses_host"}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveGuestRequiresPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-guest-g1.json")
	err := control.Save(path, control.Info{Addr: "http://127.0.0.1:1234", Token: "secret", Role: "guest", Nick: "g1", PID: os.Getpid(), SessionID: "ses_guest"})
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Fatalf("Save error = %v, want missing peer", err)
	}
}

func TestGuestPathIsPerNick(t *testing.T) {
	path, err := control.Path(t.TempDir(), "guest", "g1a2b3c4")
	if err != nil || filepath.Base(path) != "agent-guest-g1a2b3c4.json" {
		t.Fatalf("guest path = %q, %v", path, err)
	}
	path, err = control.Path(t.TempDir(), "host")
	if err != nil || filepath.Base(path) != control.HostFileName {
		t.Fatalf("host path = %q, %v", path, err)
	}
}

func TestGuestPathRejectsNickPathTraversal(t *testing.T) {
	if _, err := control.Path(t.TempDir(), "guest", "../../outside"); err == nil {
		t.Fatal("guest nick containing path separators was accepted")
	}
}

func TestLoadAgentsFindsAllGuestFiles(t *testing.T) {
	dir := t.TempDir()
	infos := []control.Info{
		{Addr: "http://127.0.0.1:1", Token: "h", Role: "host", Nick: "host", PID: os.Getpid(), SessionID: "host"},
		{Addr: "http://127.0.0.1:2", Token: "g1", Role: "guest", Nick: "g1", Peer: "host", PID: os.Getpid(), SessionID: "g1"},
		{Addr: "http://127.0.0.1:3", Token: "g2", Role: "guest", Nick: "g2", Peer: "host", PID: os.Getpid(), SessionID: "g2"},
	}
	paths := []string{filepath.Join(dir, control.HostFileName), filepath.Join(dir, "agent-guest-g1.json"), filepath.Join(dir, "agent-guest-g2.json")}
	for i := range infos {
		if err := control.Save(paths[i], infos[i]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := control.LoadAgents(dir)
	if err != nil || len(got) != 3 {
		t.Fatalf("LoadAgents = %#v, %v", got, err)
	}
}

func TestOldSingleGuestFileStillDetected(t *testing.T) {
	dir := t.TempDir()
	path, _ := control.Path(dir, "guest")
	if err := control.Save(path, control.Info{Addr: "http://127.0.0.1:1", Token: "secret", Role: "guest", Nick: "guest", Peer: "host", PID: os.Getpid(), SessionID: "ses_old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.LoadAgents(dir); err == nil || !strings.Contains(err.Error(), "jkp reset") {
		t.Fatalf("LoadAgents error = %v, want reset guidance", err)
	}
}

func TestLoadAgentsRejectsRoleMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := control.Save(filepath.Join(dir, control.HostFileName), control.Info{Addr: "http://127.0.0.1:1", Token: "secret", Role: "guest", Nick: "g1", Peer: "host", PID: os.Getpid(), SessionID: "guest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.LoadAgents(dir); err == nil || !strings.Contains(err.Error(), "role does not match") {
		t.Fatalf("LoadAgents error = %v, want role mismatch", err)
	}
}

func TestLoadAgentsSkipsDeadPIDsAcrossGuestFiles(t *testing.T) {
	dir := t.TempDir()
	if err := control.Save(filepath.Join(dir, "agent-guest-g1.json"), control.Info{Addr: "http://127.0.0.1:1", Token: "secret", Role: "guest", Nick: "g1", Peer: "host", PID: 99999999, SessionID: "guest"}); err != nil {
		t.Fatal(err)
	}
	infos, err := control.LoadAgents(dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("LoadAgents = %#v, %v; want no live agents", infos, err)
	}
}

func TestSendPassesToAndReturnsResolvedTarget(t *testing.T) {
	var gotTo string
	srv := httptest.NewServer(control.NewHandler("secret", control.Handlers{Role: "host", Send: func(to, _ string) (string, error) {
		gotTo = to
		if to == "" {
			return "g1", nil
		}
		return to, nil
	}}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := control.Save(path, control.Info{Addr: srv.URL, Token: "secret", Role: "host", Nick: "host", PID: os.Getpid(), SessionID: "host"}); err != nil {
		t.Fatal(err)
	}
	client := control.NewClient(path)
	target, err := client.Send(context.Background(), "g2", "hi")
	if err != nil || target != "g2" || gotTo != "g2" {
		t.Fatalf("Send target=%q gotTo=%q err=%v", target, gotTo, err)
	}
	target, err = client.Send(context.Background(), "", "last")
	if err != nil || target != "g1" || gotTo != "" {
		t.Fatalf("default Send target=%q gotTo=%q err=%v", target, gotTo, err)
	}
}

func TestSendRouteErrorIs409WithRoster(t *testing.T) {
	srv := httptest.NewServer(control.NewHandler("secret", control.Handlers{Role: "host", Send: func(string, string) (string, error) {
		return "", &roster.RouteError{Kind: roster.ErrNoLastSpeaker, Guests: []string{"g1", "g2"}}
	}}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := control.Save(path, control.Info{Addr: srv.URL, Token: "secret", Role: "host", Nick: "host", PID: os.Getpid(), SessionID: "host"}); err != nil {
		t.Fatal(err)
	}
	_, err := control.NewClient(path).Send(context.Background(), "", "hi")
	var routeErr *control.RouteError
	if !errors.As(err, &routeErr) || !strings.Contains(err.Error(), "g1, g2") {
		t.Fatalf("Send error = %#v, want route error with roster", err)
	}
}

func TestRosterEndpoint(t *testing.T) {
	srv := httptest.NewServer(control.NewHandler("secret", control.Handlers{Role: "host", Roster: func() roster.Snapshot {
		return roster.Snapshot{Guests: []string{"g1", "g2"}, Last: "g2"}
	}}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := control.Save(path, control.Info{Addr: srv.URL, Token: "secret", Role: "host", Nick: "host", PID: os.Getpid(), SessionID: "host"}); err != nil {
		t.Fatal(err)
	}
	snap, err := control.NewClient(path).Roster(context.Background())
	if err != nil || strings.Join(snap.Guests, ",") != "g1,g2" || snap.Last != "g2" {
		t.Fatalf("Roster = %#v, %v", snap, err)
	}
}

func TestGuestHandlerRejectsOtherTargets(t *testing.T) {
	srv := httptest.NewServer(control.NewHandler("secret", control.Handlers{Role: "guest", Peer: "host", Send: func(to, _ string) (string, error) {
		return to, nil
	}}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := control.Save(path, control.Info{Addr: srv.URL, Token: "secret", Role: "guest", Nick: "g1", Peer: "host", PID: os.Getpid(), SessionID: "guest"}); err != nil {
		t.Fatal(err)
	}
	_, err := control.NewClient(path).Send(context.Background(), "g2", "hi")
	if err == nil || !strings.Contains(err.Error(), "guests can only message the host") {
		t.Fatalf("Send error = %v, want guest target rejection", err)
	}
}

func TestSendAndRosterRequireAuthentication(t *testing.T) {
	srv := httptest.NewServer(control.NewHandler("secret", control.Handlers{Role: "host"}))
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/v1/send", "application/json", strings.NewReader(`{"body":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated send status = %d", resp.StatusCode)
	}
	resp, err = srv.Client().Get(srv.URL + "/v1/roster")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated roster status = %d", resp.StatusCode)
	}
}

func TestLoadAgentsRejectsLiveLegacySharedControlFile(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"addr":"http://127.0.0.1:1234","token":"secret","nick":"host","peer":"guest","pid":` + strconv.Itoa(os.Getpid()) + `}`
	if err := os.WriteFile(filepath.Join(dir, control.LegacyFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.LoadAgents(dir); err == nil || !strings.Contains(err.Error(), "jkp reset") {
		t.Fatalf("got %v, want reset guidance for legacy live agent", err)
	}
}

func TestLoadAgentsIgnoresDeadLegacyControlFile(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"addr":"http://127.0.0.1:1234","token":"secret","nick":"host","peer":"guest","pid":99999999}`
	if err := os.WriteFile(filepath.Join(dir, control.LegacyFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := control.LoadAgents(dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("agents = %#v, err=%v; want none", infos, err)
	}
}
