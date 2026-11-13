package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/invite"
)

func TestJoinAgentArgsForOpenCodeGuest(t *testing.T) {
	in := invite.Invite{
		Host: "192.168.0.62", Port: 6697, JoinNick: "join", JoinPassword: "guest-password",
		Peer: "host", FP: "certificate-fingerprint",
	}
	args := joinAgentArgs(in, "g1a2b3c4", "minted-password", "opencode", "", "http://127.0.0.1:4200", "ses_guest", "server-password")
	want := []string{
		"agent", "--name", "g1a2b3c4", "--harness", "opencode",
		"--oc-url", "http://127.0.0.1:4200", "--oc-session", "ses_guest", "--oc-password", "server-password",
		"--irc-host", "192.168.0.62", "--irc-port", "6697", "--irc-tls",
		"--irc-fingerprint", "certificate-fingerprint", "--irc-password", "minted-password",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("join args = %#v, want %#v", args, want)
	}
}

func TestJoinAgentArgsForClaudeGuest(t *testing.T) {
	in := invite.Invite{Host: "192.168.0.62", Port: 6697, JoinNick: "join", JoinPassword: "guest-password", Peer: "host", FP: "certificate-fingerprint"}
	for _, tc := range []struct {
		name string
		cfg  claudeJoinConfig
	}{
		{"full", claudeJoinConfig{Bin: "/usr/local/bin/claude", ForkFrom: "11111111-1111-4111-8111-111111111111", Dir: "/work/repo", StateFile: "/room/claude-g1a2b3c4.json", Model: "haiku"}},
		{"optional flags omitted", claudeJoinConfig{StateFile: "/room/claude-g1a2b3c4.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := joinClaudeAgentArgs(in, "g1a2b3c4", "minted-password", tc.cfg)
			want := []string{"agent", "--name", "g1a2b3c4", "--harness", "claude", "--claude-state-file", tc.cfg.StateFile,
				"--irc-host", in.Host, "--irc-port", "6697", "--irc-tls", "--irc-fingerprint", in.FP, "--irc-password", "minted-password"}
			for flag, value := range map[string]string{"--claude-bin": tc.cfg.Bin, "--claude-fork-from": tc.cfg.ForkFrom, "--claude-dir": tc.cfg.Dir, "--claude-model": tc.cfg.Model} {
				if value == "" {
					if slices.Contains(args, flag) {
						t.Errorf("empty optional flag %s present: %#v", flag, args)
					}
				} else {
					want = append(want, flag, value)
				}
			}
			for i := 0; i < len(want); i++ {
				if want[i] == "agent" || want[i] == "--irc-tls" {
					if !slices.Contains(args, want[i]) {
						t.Errorf("args missing %q: %#v", want[i], args)
					}
					continue
				}
				if index := slices.Index(args, want[i]); index < 0 || index+1 >= len(args) || args[index+1] != want[i+1] {
					t.Errorf("flag %q not followed by %q: %#v", want[i], want[i+1], args)
				}
				i++
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "--announce") {
					t.Errorf("join must be silent, got %q", arg)
				}
			}
		})
	}
}

func TestResolveClaudeJoinSessionHandlesPlaceholder(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		forkFrom, controlSession, resolvedForkFrom string
		warned                                     bool
	}{
		{session, session, session, false},
		{"", "claude-g1", "", true},
		{"${CLAUDE_SESSION_ID}", "claude-g1", "", true},
	} {
		t.Run(tc.forkFrom, func(t *testing.T) {
			controlSession, resolvedForkFrom, warned := resolveClaudeJoinSession(tc.forkFrom, "g1")
			if controlSession != tc.controlSession || resolvedForkFrom != tc.resolvedForkFrom || warned != tc.warned {
				t.Fatalf("resolveClaudeJoinSession = (%q, %q, %t), want (%q, %q, %t)", controlSession, resolvedForkFrom, warned, tc.controlSession, tc.resolvedForkFrom, tc.warned)
			}
		})
	}
}

func TestControlAgentArgsWithLogFile(t *testing.T) {
	args, err := controlAgentArgsWithLog(t.TempDir(), "guest", "g1a2b3c4", "host", "ses-c", "/tmp/g1.log")
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(args, "--control-log")
	if index < 0 || index+1 >= len(args) || args[index+1] != "/tmp/g1.log" {
		t.Fatalf("control args missing log path: %#v", args)
	}
}

func TestWriteJoinResultForClaudeIncludesPrivacyNoteAndLogHint(t *testing.T) {
	var out bytes.Buffer
	writeClaudeJoinResult(&out, "g1a2b3c4", "host", true, "/tmp/jkp-guest-g1a2b3c4.log")
	for _, want := range []string{"joined as g1a2b3c4", "silent", "conversation", "jkp log"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("join output missing %q: %q", want, out.String())
		}
	}
}

func TestJoinAgentArgsForPiGuest(t *testing.T) {
	in := invite.Invite{Host: "10.0.0.2", Port: 6697, JoinNick: "join", JoinPassword: "pw", Peer: "host", FP: "fp"}
	args := joinAgentArgs(in, "g1a2b3c4", "minted-password", "pi", "pi-session", "", "", "")
	if !slices.Contains(args, "--harness") || !slices.Contains(args, "pi") || !slices.Contains(args, "--pi-session") || !slices.Contains(args, "pi-session") {
		t.Fatalf("pi join args missing harness or session: %#v", args)
	}
}

func TestJoinOutputIsSilent(t *testing.T) {
	var out bytes.Buffer
	writeJoinResult(&out, "g1a2b3c4", "host", "", "/tmp/jkp-guest-g1a2b3c4.log")
	if !strings.Contains(out.String(), "joined as g1a2b3c4") || !strings.Contains(out.String(), "Join is silent; use jkp say to talk to the host") {
		t.Fatalf("join output = %q", out.String())
	}
}

func TestGuestControlFileIsPerMintedNick(t *testing.T) {
	dir := t.TempDir()
	args, err := controlAgentArgs(dir, "guest", "g1a2b3c4", "host", "session-b")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--peer", "host", "--control-file", filepath.Join(dir, "agent-guest-g1a2b3c4.json"), "--control-session", "session-b", "--control-role", "guest"}
	if !slices.Equal(args, want) {
		t.Fatalf("control args = %#v, want %#v", args, want)
	}
}

func TestOpenCodeCommandRoutesHostAndInviteArguments(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "integrations", "opencode", "jkp.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{"$ARGUMENTS", "exactly `host`", "jkp host", "jkp join --invite", "--harness opencode", "jkp say", "say --to", "jkp reset", "jkp help", "jkp log", "retry", "already bound"} {
		if !strings.Contains(text, want) {
			t.Errorf("OpenCode command missing %q", want)
		}
	}
}

func TestOpenCodeJkpSkillTeachesSayAndSelfDiscovery(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "integrations", "opencode", "jkp-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{"jkp help", "jkp say --", "jkp say --to", "last guest who spoke", "joining is silent", "jkp reset", "Claude", "jkp log", "retry", "already bound"} {
		if !strings.Contains(text, want) {
			t.Errorf("jkp skill missing %q", want)
		}
	}
	if strings.Contains(text, "[jkp-send") {
		t.Fatal("skill should not teach jkp-send as an initiation command")
	}
}

func TestPiExtensionExposesSayResetAndHelp(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "extensions", "pi-jkp", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{`case "say"`, `case "reset"`, `case "help"`, `"say", "--"`, `"--to"`, `say --to`} {
		if !strings.Contains(text, want) {
			t.Errorf("pi extension missing %q", want)
		}
	}
}

func TestClaudeCommandTeachesSharedSilentJoin(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "integrations", "claude", "jkp.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"same invite", "silent", "jkp say --to", "jkp help",
		"--harness claude", "--claude-fork-from", "${CLAUDE_SESSION_ID}",
		"--claude-dir", "${CLAUDE_PROJECT_DIR}", "jkp say --session",
		"jkp log --session", "jkp reset", "retry", "already bound",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Claude command missing %q", want)
		}
	}
	for _, unwanted := range []string{"--harness pi", "--pi-session claude"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("Claude command must not contain %q", unwanted)
		}
	}
}

func TestReadmeDescribesClaudeGuestForkAndPrivacy(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{"background Claude", "fork", "/jkp log", "opencode --port 4096"} {
		if !strings.Contains(text, want) {
			t.Errorf("README missing %q", want)
		}
	}
	if !strings.Contains(strings.ToLower(text), "privacy") {
		t.Error("README missing privacy note about the conversation reaching the host")
	}
	if strings.Contains(text, "Claude Code is not yet a bound participant") {
		t.Error("README must not describe Claude Code as not yet a bound participant")
	}
}
