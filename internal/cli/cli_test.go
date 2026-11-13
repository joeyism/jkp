package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/cli"
	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/protocol"
)

type syncClock struct{}

func (syncClock) Now() time.Time { return time.Now() }

type discardSender struct{}

func (discardSender) Send(protocol.OutboundMessage) error { return nil }

func newCLI(t *testing.T) (*cli.CLI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	svc := core.NewService(discardSender{}, syncClock{})
	var out, errOut bytes.Buffer
	return cli.New(svc, &out, &errOut), &out, &errOut
}

func TestEndpointRegisterThenList(t *testing.T) {
	t.Parallel()
	c, out, _ := newCLI(t)

	if err := c.Run([]string{
		"endpoint", "register",
		"--id", "oc-author", "--harness", "opencode",
		"--session-id", "ses_123", "--nick", "oc-author",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run([]string{"endpoint", "list"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "oc-author") || !strings.Contains(got, "ses_123") {
		t.Fatalf("list must show the endpoint, got %q", got)
	}
}

func TestEndpointEnableDisable(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	if err := c.Run([]string{"endpoint", "register",
		"--id", "a", "--harness", "pi", "--session-id", "s", "--nick", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run([]string{"endpoint", "enable", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Run([]string{"endpoint", "disable", "a"}); err != nil {
		t.Fatal(err)
	}
}

func TestServeWithoutTokenFailsClosed(t *testing.T) {
	t.Parallel()
	c, _, errOut := newCLI(t)
	err := c.Run([]string{"serve"})
	if err == nil {
		t.Fatal("serve without a token must fail closed")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error should mention the missing token, got %q", err)
	}
	_ = errOut
}

func TestRegisterMissingRequiredFlagsFails(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	if err := c.Run([]string{"endpoint", "register", "--id", "a"}); err == nil {
		t.Fatal("register without harness/session/nick must fail")
	}
}

func TestUnknownCommandFails(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	if err := c.Run([]string{"frobnicate"}); err == nil {
		t.Fatal("unknown command must fail")
	}
}

func TestHelpDescribesMultiGuestRooms(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{
		"one host, many guests",
		"every guest uses the same invite",
		"guests never see each other",
		"joining is silent",
		"jkp say -- <text>", "last guest who spoke",
		"jkp say --to <nick> -- <text>",
		"lists connected guests",
		"not sent",
		"guest jkp say always goes to the host",
		"[JKP-LOCAL-ONLY]", "START", "16384 UTF-8 bytes",
		"no fan-out yet",
		"old invite", "jkp reset", "jkp host",
	} {
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("jkp help missing %q", phrase)
		}
	}
}

func TestHelpRequiresServedOpenCode(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "opencode --port 4096") {
		t.Fatalf("jkp help must document served OpenCode setup: %q", out.String())
	}
}

func TestHelpDescribesClaudeGuestLogAndPrivacy(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"opencode|pi|claude", "jkp log", "--session", "fork", "conversation"} {
		if !strings.Contains(out.String(), phrase) {
			t.Errorf("jkp help missing %q", phrase)
		}
	}
}

func TestEnableUnknownEndpointFails(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	if err := c.Run([]string{"endpoint", "enable", "ghost"}); err == nil {
		t.Fatal("enabling an unknown endpoint must fail")
	}
}

func TestDuplicateRegisterFails(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	args := []string{"endpoint", "register",
		"--id", "a", "--harness", "pi", "--session-id", "s", "--nick", "a"}
	if err := c.Run(args); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(args); err == nil {
		t.Fatal("duplicate registration must fail")
	}
}

func TestAgentRejectsTLSWithoutFingerprint(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{
		"agent", "--name", "pi", "--harness", "pi",
		"--pi-session", "s", "--irc-password", "pw",
		"--irc-tls",
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentHostRoleAllowsNoPeer(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{
		"agent", "--name", "host", "--harness", "opencode", "--oc-session", "session", "--oc-url", "http://127.0.0.1:1",
		"--irc-password", "pw", "--irc-host", "127.0.0.1", "--irc-port", "1",
		"--control-role", "host", "--control-file", filepath.Join(t.TempDir(), "host.json"), "--control-session", "session",
		"--mint-guests", "--join-nick", "join",
	})
	if err == nil || strings.Contains(err.Error(), "usage error") || strings.Contains(err.Error(), "--peer") {
		t.Fatalf("host agent should accept no fixed peer before IRC connection; err=%v", err)
	}
}

func TestAgentAcceptsClaudeHarnessFlags(t *testing.T) {
	c, _, _ := newCLI(t)
	dir := t.TempDir()
	c.Env = func(string) string { return "" }
	err := c.Run([]string{
		"agent", "--name", "g1", "--harness", "claude",
		"--claude-fork-from", "11111111-1111-4111-8111-111111111111",
		"--claude-dir", dir, "--claude-state-file", filepath.Join(dir, "claude-g1.json"), "--claude-model", "haiku",
		"--irc-password", "pw", "--irc-host", "127.0.0.1", "--irc-port", "1", "--peer", "host",
		"--control-role", "guest", "--control-file", filepath.Join(dir, "agent-guest-g1.json"),
		"--control-session", "11111111-1111-4111-8111-111111111111", "--control-log", filepath.Join(dir, "g1.log"),
	})
	if err == nil || strings.Contains(err.Error(), "usage error") {
		t.Fatalf("Claude flags should parse before IRC connection fails; err=%v", err)
	}
}

func TestAgentGuestRoleRequiresPeer(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{
		"agent", "--name", "g1", "--harness", "pi", "--pi-session", "session",
		"--irc-password", "pw", "--control-role", "guest", "--control-file", filepath.Join(t.TempDir(), "guest.json"), "--control-session", "session",
	})
	if err == nil || !strings.Contains(err.Error(), "--peer") {
		t.Fatalf("guest agent without peer error = %v", err)
	}
}

func TestAgentMintGuestsRequiresJoinNick(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{
		"agent", "--name", "host", "--harness", "opencode", "--oc-session", "session", "--oc-url", "http://127.0.0.1:1",
		"--irc-password", "pw", "--control-role", "host", "--control-file", filepath.Join(t.TempDir(), "host.json"), "--control-session", "session",
		"--mint-guests", "--join-nick", "",
	})
	if err == nil || !strings.Contains(err.Error(), "--join-nick") {
		t.Fatalf("guest minting without join nick error = %v", err)
	}
}

func TestAgentAcceptsReplyTimeoutOverride(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{"agent", "--oc-reply-timeout", "150ms"})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("got %v, want normal agent validation after parsing timeout flag", err)
	}
}

func TestJoinRequiresInvite(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{"join"})
	if err == nil {
		t.Fatal("expected usage error")
	}
}

func TestJoinRequiresHarness(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{"join", "--invite", "jkp1.invalid"})
	if err == nil || !strings.Contains(err.Error(), "--harness") {
		t.Fatalf("got %v, want missing-harness usage error", err)
	}
}

func TestJoinPiRequiresSession(t *testing.T) {
	c, _, _ := newCLI(t)
	err := c.Run([]string{"join", "--invite", "jkp1.invalid", "--harness", "pi"})
	if err == nil || !strings.Contains(err.Error(), "--pi-session") {
		t.Fatalf("got %v, want missing-pi-session usage error", err)
	}
}

func TestJoinOpenCodeWithoutSessionFailsBeforeNetworkDial(t *testing.T) {
	c, _, _ := newCLI(t)
	c.RoomDir = func() (string, error) { return t.TempDir(), nil }
	c.Env = func(key string) string {
		if key == "JKP_OC_URL" {
			return "http://127.0.0.1:1"
		}
		return ""
	}
	token, err := invite.Encode(invite.Invite{
		Host: "127.0.0.1", Port: 6697, JoinNick: "join", JoinPassword: "pw", Peer: "host",
		FP: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Run([]string{"join", "--invite", token, "--harness", "opencode"})
	if err == nil || !strings.Contains(err.Error(), "opencode --port 4096") {
		t.Fatalf("got %v, want served-OpenCode setup guidance", err)
	}
}

func TestHelpListsUserCommandsButNotAgentInternals(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"jkp host", "jkp join", "jkp say", "jkp reset", "jkp version"} {
		if !strings.Contains(got, want) {
			t.Errorf("help missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "jkp agent") || strings.Contains(got, "go build") {
		t.Fatalf("help exposed internal/developer commands: %s", got)
	}
}

func TestNoArgumentsShowsHelp(t *testing.T) {
	c, out, _ := newCLI(t)
	if err := c.Run(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "jkp say") || !strings.Contains(out.String(), "jkp reset") {
		t.Fatalf("no-argument output is not help: %s", out.String())
	}
}

func TestResetRemovesRoomAndAgentState(t *testing.T) {
	c, _, _ := newCLI(t)
	dir := t.TempDir()
	c.RoomDir = func() (string, error) { return dir, nil }
	for _, name := range []string{"room.json", control.HostFileName, control.GuestFileName, control.LegacyFileName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"agent_pid":99999999}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Run([]string{"reset"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"room.json", control.HostFileName, control.GuestFileName, control.LegacyFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s remains after reset: %v", name, err)
		}
	}
}

func TestResetWithNoRoomStateSucceeds(t *testing.T) {
	c, _, _ := newCLI(t)
	c.RoomDir = func() (string, error) { return t.TempDir(), nil }
	if err := c.Run([]string{"reset"}); err != nil {
		t.Fatal(err)
	}
}

func TestSayWithoutActiveAgentExplainsHowToJoin(t *testing.T) {
	c, _, _ := newCLI(t)
	c.RoomDir = func() (string, error) { return t.TempDir(), nil }
	err := c.Run([]string{"say", "--", "hello"})
	if err == nil || !strings.Contains(err.Error(), "not in a room") || !strings.Contains(err.Error(), "jkp help") {
		t.Fatalf("got %v, want not-in-room help error", err)
	}
}

func TestVersionPrintsBuildVersion(t *testing.T) {
	c, out, _ := newCLI(t)
	cli.Version = "ci-test"
	t.Cleanup(func() { cli.Version = "dev" })
	if err := c.Run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "ci-test" {
		t.Fatalf("version %q", out.String())
	}
}
