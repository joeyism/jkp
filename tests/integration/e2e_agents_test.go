//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinclient"
)

// TestE2EHostGuestAgentsAndSay exercises the real CLI agent processes, Ergo
// transport, control-file `jkp say`, reassembly, ACK filtering, and timeout
// handling. Only the OpenCode servers are faked; no model is called.
func TestE2EHostGuestAgentsAndSay(t *testing.T) {
	ergo := ergoAddr(t)
	hostAddr, portText, err := net.SplitHostPort(ergo)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	hostNick, joinNick := "e2eh"+suffix, "e2ej"+suffix
	hostPass, joinPass := "host-"+suffix, "join-"+suffix
	registerTestAccount(t, ergo, hostNick, hostPass)
	registerTestAccount(t, ergo, joinNick, joinPass)
	var minted []string
	t.Cleanup(func() {
		for _, nick := range minted {
			eraseTestAccount(t, ergo, nick)
		}
	})

	const hostSession, guestSession = "ses-e2e-host", "ses-e2e-guest"
	// Force multibyte runes across several IRC chunk boundaries; Ergo rejects
	// any individual PRIVMSG that is not valid UTF-8. The legacy opt-out marker
	// is a sign-off here and must not suppress this peer-facing answer.
	longAnswer := strings.TrimSpace(strings.Repeat("facing left” — dimensions 2000×2000 🚀. ", 180))
	longReply := longAnswer + "\n\nNo further reply is needed. [JKP-NOREPLY]"
	localPlan := "[JKP-LOCAL-ONLY]\nPlan for my user: " + strings.Repeat("inspect the next candidate and preserve its provenance. ", 12)
	hostOC := newScriptedOpenCode(t, hostSession, "host-secret", func(turn int, _ string) (string, bool) {
		if turn == 1 {
			return "Which selected video path should I verify?", true
		}
		if turn == 2 {
			return "Thanks for the handoff. Should I inspect the next candidate now?", true
		}
		return "Acknowledged.", true
	})
	guestOC := newScriptedOpenCode(t, guestSession, "guest-secret", func(turn int, _ string) (string, bool) {
		if turn == 1 {
			return longReply, true
		}
		if turn == 2 {
			return localPlan, true
		}
		return "", false // the one-way notice and timeout-check do not produce a peer reply
	})

	bin := buildE2EBinary(t)
	stateDir := t.TempDir()

	hostAgent := startE2EAgent(t, bin, stateDir, []string{
		"agent", "--name", hostNick, "--harness", "opencode",
		"--oc-url", hostOC.URL(), "--oc-password", "host-secret", "--oc-session", hostSession,
		"--oc-reply-timeout", "350ms",
		"--irc-host", hostAddr, "--irc-port", strconv.Itoa(port), "--irc-password", hostPass,
		"--control-role", "host",
		"--control-file", filepath.Join(stateDir, "agent-host.json"), "--control-session", hostSession,
		"--mint-guests", "--join-nick", joinNick, "--oper-addr", ergo, "--oper-user", operUser, "--oper-pass", operPass,
	})
	waitForFile(t, filepath.Join(stateDir, "agent-host.json"), 5*time.Second)
	tok, err := invite.Encode(invite.Invite{Host: hostAddr, Port: port, JoinNick: joinNick, JoinPassword: joinPass, Peer: hostNick, FP: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	sharedInvite, err := invite.Decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	guestNick, guestPass, err := joinclient.RequestGrant(context.Background(), func(cfg irc.RealConfig) joinclient.Client {
		cfg.TLS = false // this E2E uses Ergo on loopback; public jkp join pins TLS.
		cfg.CertFP = ""
		return irc.NewRealClient(cfg)
	}, sharedInvite)
	if err != nil {
		t.Fatal(err)
	}
	minted = append(minted, guestNick)
	guestAgent := startE2EAgent(t, bin, stateDir, []string{
		"agent", "--name", guestNick, "--harness", "opencode",
		"--oc-url", guestOC.URL(), "--oc-password", "guest-secret", "--oc-session", guestSession,
		"--oc-reply-timeout", "350ms",
		"--irc-host", hostAddr, "--irc-port", strconv.Itoa(port), "--irc-password", guestPass,
		"--peer", hostNick, "--control-role", "guest",
		"--control-file", filepath.Join(stateDir, controlGuestFile(guestNick)), "--control-session", guestSession,
	})
	t.Cleanup(func() { stopE2EAgent(hostAgent); stopE2EAgent(guestAgent) })
	waitForFile(t, filepath.Join(stateDir, controlGuestFile(guestNick)), 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if got := hostOC.UserCount(); got != 0 {
		t.Fatalf("silent join injected %d turns into host OpenCode", got)
	}
	output := runE2ESay(t, bin, stateDir, guestSession, "return the full report")
	if !strings.Contains(output, "sent to "+hostNick+" as "+guestNick) {
		t.Fatalf("guest say output = %q", output)
	}
	initial := hostOC.WaitUser(ctx)
	if !strings.Contains(initial, guestNick+":") || !strings.Contains(initial, "return the full report") {
		t.Fatalf("host did not receive guest's first message: %q", initial)
	}
	guestPrompt := guestOC.WaitUser(ctx)
	if !strings.Contains(guestPrompt, "Which selected video path") {
		t.Fatalf("guest did not receive host reply: %q", guestPrompt)
	}
	longAtHost := hostOC.WaitUser(ctx)
	if !strings.Contains(longAtHost, longAnswer) || strings.Contains(longAtHost, "JKP-NOREPLY") {
		for _, name := range []string{hostNick + ".log", guestNick + ".log"} {
			if b, err := os.ReadFile(filepath.Join(stateDir, name)); err == nil {
				t.Logf("%s: %s", name, b)
			}
		}
		t.Fatalf("long guest response did not arrive intact (got=%d want=%d contains=%v suffix=%q); hostCount=%d guestCount=%d", len(longAtHost), len(longAnswer), strings.Contains(longAtHost, longAnswer), longAtHost[len(longAtHost)-min(len(longAtHost), 100):], hostOC.UserCount(), guestOC.UserCount())
	}
	localOnlyInput := guestOC.WaitUser(ctx)
	if !strings.Contains(localOnlyInput, "Should I inspect the next candidate") {
		t.Fatalf("guest did not receive host's follow-up: %q", localOnlyInput)
	}
	deadline := time.Now().Add(3 * time.Second)
	noticeSeen := false
	for time.Now().Before(deadline) {
		for _, input := range guestOC.AllUsers() {
			if strings.Contains(input, "[JKP-NOTICE]") && strings.Contains(input, "kept local") {
				noticeSeen = true
				break
			}
		}
		if noticeSeen {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !noticeSeen {
		t.Fatalf("guest did not receive one-way notice for its local-only turn; guest inputs=%q", guestOC.AllUsers())
	}
	time.Sleep(250 * time.Millisecond) // local-only guest turn and its notice response must stay local
	if got := hostOC.UserCount(); got != 2 {
		t.Fatalf("guest local-only turn or notice response was forwarded; host received %d injects", got)
	}
	time.Sleep(time.Second) // let Ergo's per-client message flood window expire
	if output := runE2ESay(t, bin, stateDir, hostSession, "timeout-check"); !strings.Contains(output, "sent to "+guestNick+" as "+hostNick) {
		t.Fatalf("timeout probe say output = %q", output)
	}
	_ = guestOC.WaitUser(ctx)
	time.Sleep(700 * time.Millisecond) // longer than the configured harness timeout
	if got := hostOC.UserCount(); got != 2 {
		t.Fatalf("guest timeout error was forwarded; host received %d injects", got)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(cwd, "../.."))
}

func registerTestAccount(t *testing.T, addr, nick, pass string) {
	t.Helper()
	operNick := "op" + nick
	oper := dialRaw(t, addr, operNick)
	t.Cleanup(oper.close)
	oper.send("OPER " + operUser + " " + operPass)
	if line := oper.waitContains(" 381 ", 8*time.Second); !strings.Contains(line, "381") {
		t.Fatalf("OPER failed for test account: %q", line)
	}
	oper.send("NICKSERV SAREGISTER " + nick + " " + pass)
	if line := oper.waitContains("Successfully registered account "+nick, 8*time.Second); !strings.Contains(line, "Successfully registered account "+nick) {
		t.Fatalf("SAREGISTER %s failed: %q", nick, line)
	}
	t.Cleanup(func() { eraseTestAccount(t, addr, nick) })
}

func eraseTestAccount(t *testing.T, addr, nick string) {
	t.Helper()
	oper := dialRaw(t, addr, "cleanup"+nick)
	defer oper.close()
	oper.send("OPER " + operUser + " " + operPass)
	if line := oper.waitContains(" 381 ", 8*time.Second); !strings.Contains(line, "381") {
		t.Errorf("cleanup OPER failed for %s: %q", nick, line)
		return
	}
	oper.send("NICKSERV ERASE " + nick)
	line := oper.waitContains("To confirm, run this command:", 8*time.Second)
	marker := "/NS ERASE " + nick + " "
	idx := strings.LastIndex(line, marker)
	if idx < 0 {
		t.Errorf("could not get ERASE confirmation for %s: %q", nick, line)
		return
	}
	fields := strings.Fields(line[idx+len(marker):])
	if len(fields) == 0 {
		t.Errorf("ERASE confirmation code missing for %s: %q", nick, line)
		return
	}
	oper.send("NICKSERV ERASE " + nick + " " + fields[0])
	if line := oper.waitContains("Successfully unregistered account "+nick, 8*time.Second); !strings.Contains(line, "Successfully unregistered account "+nick) {
		t.Errorf("ERASE %s failed: %q", nick, line)
	}
}

func startE2EAgent(t *testing.T, bin, stateDir string, args []string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "JKP_ROOM_DIR="+stateDir)
	log, err := os.Create(filepath.Join(stateDir, args[2]+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return cmd
}

func stopE2EAgent(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".log") {
			if data, err := os.ReadFile(filepath.Join(filepath.Dir(path), entry.Name())); err == nil {
				t.Logf("%s: %s", entry.Name(), data)
			}
		}
	}
	t.Fatalf("timed out waiting for %s", path)
}

func runE2ESay(t *testing.T, bin, stateDir, session, body string) string {
	t.Helper()
	cmd := exec.Command(bin, "say", "--", body)
	cmd.Env = append(os.Environ(), "JKP_ROOM_DIR="+stateDir, "OPENCODE_SESSION_ID="+session)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jkp say: %v\n%s", err, out)
	}
	return string(out)
}
