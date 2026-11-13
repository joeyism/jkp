package host_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/host"
	"github.com/joey/jkp/internal/invite"
)

func TestHostPrintsInviteAndReusesRoom(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var starts int
	var registered []string
	var agentArgs []string
	h := host.New(host.Deps{
		RoomDir:     dir,
		LANIP:       func() (string, error) { return "192.168.0.62", nil },
		EnsureErgo:  func() error { return nil },
		Fingerprint: func() (string, error) { return strings.Repeat("cd", 32), nil },
		Register:    func(nick, pass string) error { registered = append(registered, nick); return nil },
		StartAgent: func(args []string) (int, error) {
			starts++
			agentArgs = append([]string(nil), args...)
			return 4242, nil
		},
		AgentAlive:      func(pid int) bool { return pid == 4242 && starts > 0 },
		OpenCodeURL:     "http://127.0.0.1:4200",
		OpenCodeSession: "ses_test",
	})
	var out bytes.Buffer
	if err := h.Run(&out); err != nil {
		t.Fatal(err)
	}
	if err := h.Run(&out); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("agent starts = %d, want 1", starts)
	}
	if strings.Count(out.String(), "INVITE jkp1.") != 2 {
		t.Fatalf("expected 2 INVITE lines, got:\n%s", out.String())
	}
	if strings.Join(registered, ",") != "host,join" {
		t.Fatalf("registered nicks = %v, want host, join", registered)
	}
	if strings.Join(agentArgs, " ") == "" || !strings.Contains(strings.Join(agentArgs, " "), "--name host") {
		t.Fatalf("host agent args do not use host role: %v", agentArgs)
	}
	joinedArgs := strings.Join(agentArgs, " ")
	if !strings.Contains(joinedArgs, filepath.Join(dir, "agent-host.json")) || !strings.Contains(joinedArgs, "--control-session ses_test") {
		t.Fatalf("host agent args do not record its role/session: %v", agentArgs)
	}
	if !strings.Contains(out.String(), "Guests join with this invite") || strings.Contains(out.String(), "Peer is guest") {
		t.Fatalf("room output should describe a shared guest invite:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "192.168.0.62") {
		t.Fatalf("invite missing LAN IP:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "ses_test") {
		t.Fatalf("invite missing session:\n%s", out.String())
	}
	inviteStart := strings.Index(out.String(), "INVITE ")
	if inviteStart < 0 {
		t.Fatalf("missing invite in output:\n%s", out.String())
	}
	token := strings.Fields(out.String()[inviteStart:])[1]
	decoded, err := invite.Decode(token)
	if err != nil || decoded.JoinNick != "join" || decoded.JoinPassword == "" {
		t.Fatalf("decoded invite = %#v err=%v", decoded, err)
	}
	joinedArgs = strings.Join(agentArgs, " ")
	if strings.Contains(joinedArgs, "--peer") || !strings.Contains(joinedArgs, "--mint-guests") || !strings.Contains(joinedArgs, "--join-nick join") {
		t.Fatalf("host agent args should mint guests without a fixed peer: %v", agentArgs)
	}
}

func TestHostSelfHealsPreMultiGuestRoom(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"host_nick":"host","host_password":"old-host","guest_nick":"guest","guest_password":"old-guest","fp":"` + strings.Repeat("ab", 32) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "room.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-guest.json"), []byte(`{"pid":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var registered []string
	var starts int
	h := host.New(host.Deps{
		RoomDir:     dir,
		LANIP:       func() (string, error) { return "192.168.0.62", nil },
		EnsureErgo:  func() error { return nil },
		Fingerprint: func() (string, error) { return strings.Repeat("ab", 32), nil },
		Register: func(nick, pass string) error {
			if pass == "" {
				t.Fatal("empty password")
			}
			registered = append(registered, nick)
			return nil
		},
		StartAgent:      func(args []string) (int, error) { starts++; return 4242, nil },
		AgentAlive:      func(pid int) bool { return pid == 4242 && starts > 0 },
		OpenCodeURL:     "http://127.0.0.1:4200",
		OpenCodeSession: "ses_test",
	})
	var out bytes.Buffer
	err := h.Run(&out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cleared leftover", "INVITE jkp1."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("host output missing %q: %s", want, out.String())
		}
	}
	if strings.Join(registered, ",") != "host,join" {
		t.Errorf("registered nicks = %v, want host, join", registered)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-guest.json")); !os.IsNotExist(err) {
		t.Errorf("stale agent-guest.json still exists or stat failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "room.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"join_nick": "join"`) {
		t.Errorf("room missing fresh join identity: %s", data)
	}
}

func TestHostFailsWithoutLANIP(t *testing.T) {
	t.Parallel()
	h := host.New(host.Deps{
		RoomDir:     t.TempDir(),
		LANIP:       func() (string, error) { return "", nil },
		EnsureErgo:  func() error { return nil },
		Fingerprint: func() (string, error) { return strings.Repeat("ab", 32), nil },
		Register:    func(nick, pass string) error { return nil },
		StartAgent:  func(args []string) (int, error) { return 1, nil },
		AgentAlive:  func(pid int) bool { return true },
	})
	var out bytes.Buffer
	if err := h.Run(&out); err == nil {
		t.Fatal("want error when LANIP returns empty")
	}
}

func TestHostDoesNotTellGuestToCompile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := host.New(host.Deps{
		RoomDir:         dir,
		LANIP:           func() (string, error) { return "192.168.0.62", nil },
		EnsureErgo:      func() error { return nil },
		Fingerprint:     func() (string, error) { return strings.Repeat("cd", 32), nil },
		Register:        func(nick, pass string) error { return nil },
		StartAgent:      func(args []string) (int, error) { return 4242, nil },
		AgentAlive:      func(pid int) bool { return pid == 4242 },
		OpenCodeURL:     "http://127.0.0.1:4200",
		OpenCodeSession: "ses_test",
	})
	var out bytes.Buffer
	if err := h.Run(&out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "go build") {
		t.Fatalf("host told the guest to compile:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "/jkp jkp1.") || !strings.Contains(out.String(), "OpenCode or pi") {
		t.Fatalf("host did not print harness-neutral guest instructions:\n%s", out.String())
	}
}

func TestHostSelfHealsLegacyOcPiRoomWithFreshCredentials(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"oc_nick":"oc","oc_password":"old-host","pi_nick":"pi","pi_password":"old-guest"}`
	if err := os.WriteFile(filepath.Join(dir, "room.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var registered []string
	var credentials [][2]string
	var starts int
	h := host.New(host.Deps{
		RoomDir: dir, EnsureErgo: func() error { return nil },
		LANIP:       func() (string, error) { return "192.168.0.62", nil },
		Fingerprint: func() (string, error) { return strings.Repeat("ab", 32), nil },
		Register: func(nick, pass string) error {
			if pass == "" || pass == "old-host" || pass == "old-guest" {
				t.Fatalf("registration for %q did not use a fresh password", nick)
			}
			registered = append(registered, nick)
			credentials = append(credentials, [2]string{nick, pass})
			return nil
		},
		StartAgent:      func(args []string) (int, error) { starts++; return 4242, nil },
		AgentAlive:      func(pid int) bool { return pid == 4242 && starts > 0 },
		OpenCodeURL:     "http://127.0.0.1:4200",
		OpenCodeSession: "ses_test",
	})
	var out bytes.Buffer
	err := h.Run(&out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(registered, ",") != "host,join" {
		t.Errorf("registered nicks = %v, want host, join", registered)
	}
	if len(credentials) != 2 {
		t.Errorf("recorded %d credential pairs, want 2", len(credentials))
	}
	for _, want := range []string{"cleared leftover", "INVITE jkp1."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("host output missing %q: %s", want, out.String())
		}
	}
}
