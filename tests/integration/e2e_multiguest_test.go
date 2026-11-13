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

func TestE2EMultiGuest(t *testing.T) {
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

	roomInvite, err := invite.Encode(invite.Invite{
		Host: hostAddr, Port: port, JoinNick: joinNick, JoinPassword: joinPass,
		Peer: hostNick, FP: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedInvite, err := invite.Decode(roomInvite)
	if err != nil {
		t.Fatal(err)
	}

	suffix = fmt.Sprintf("%x", time.Now().UnixNano())
	hostSession, guest1Session, guest2Session := "host-"+suffix, "guest1-"+suffix, "guest2-"+suffix
	hostOC := newScriptedOpenCode(t, hostSession, "host-secret", func(_ int, _ string) (string, bool) {
		return "Host reply.", true
	})
	guest1OC := newScriptedOpenCode(t, guest1Session, "guest1-secret", func(_ int, _ string) (string, bool) {
		return "Acknowledged.", true
	})
	guest2OC := newScriptedOpenCode(t, guest2Session, "guest2-secret", func(_ int, _ string) (string, bool) {
		return "Acknowledged.", true
	})
	bin := buildE2EBinary(t)
	stateDir := t.TempDir()

	hostAgent := startE2EAgent(t, bin, stateDir, []string{
		"agent", "--name", hostNick, "--harness", "opencode",
		"--oc-url", hostOC.URL(), "--oc-password", "host-secret", "--oc-session", hostSession,
		"--irc-host", hostAddr, "--irc-port", strconv.Itoa(port), "--irc-password", hostPass,
		"--control-role", "host", "--control-file", filepath.Join(stateDir, "agent-host.json"), "--control-session", hostSession,
		"--mint-guests", "--join-nick", joinNick, "--oper-addr", ergo,
		"--oper-user", operUser, "--oper-pass", operPass,
	})
	t.Cleanup(func() { stopE2EAgent(hostAgent) })
	waitForFile(t, filepath.Join(stateDir, "agent-host.json"), 5*time.Second)
	// Use the same shared-invite exchange over loopback plaintext IRC. The
	// production join command still enforces TLS and certificate pinning.
	newBootstrapClient := func(cfg irc.RealConfig) joinclient.Client {
		cfg.TLS = false
		cfg.CertFP = ""
		return irc.NewRealClient(cfg)
	}
	type grantResult struct {
		nick string
		pass string
		err  error
	}
	grants := make(chan grantResult, 2)
	for range 2 {
		go func() {
			nick, pass, err := joinclient.RequestGrant(context.Background(), newBootstrapClient, sharedInvite)
			grants <- grantResult{nick: nick, pass: pass, err: err}
		}()
	}
	var credentials []grantResult
	for range 2 {
		select {
		case grant := <-grants:
			if grant.err != nil {
				t.Fatalf("shared invite join: %v", grant.err)
			}
			credentials = append(credentials, grant)
			minted = append(minted, grant.nick)
		case <-time.After(10 * time.Second):
			if data, err := os.ReadFile(filepath.Join(stateDir, hostNick+".log")); err == nil {
				t.Logf("host agent log: %s", data)
			}
			t.Fatal("timed out waiting for concurrent sealed guest grants")
		}
	}
	if len(credentials) != 2 || credentials[0].nick == credentials[1].nick {
		t.Fatalf("concurrent joins got duplicate guest identities: %#v", credentials)
	}
	if got := hostOC.UserCount(); got != 0 {
		t.Fatalf("silent joins injected %d turns into host OpenCode", got)
	}

	guestAgents := make([]*exec.Cmd, 2)
	guestOCs := []*scriptedOpenCode{guest1OC, guest2OC}
	guestSessions := []string{guest1Session, guest2Session}
	guestNicknames := []string{credentials[0].nick, credentials[1].nick}
	for i, credential := range credentials {
		roleFile := filepath.Join(stateDir, controlGuestFile(credential.nick))
		agent := startE2EAgent(t, bin, stateDir, []string{
			"agent", "--name", credential.nick, "--harness", "opencode",
			"--oc-url", guestOCs[i].URL(), "--oc-password", []string{"guest1-secret", "guest2-secret"}[i], "--oc-session", guestSessions[i],
			"--irc-host", hostAddr, "--irc-port", strconv.Itoa(port), "--irc-password", credential.pass,
			"--peer", hostNick, "--control-role", "guest", "--control-file", roleFile, "--control-session", guestSessions[i],
		})
		guestAgents[i] = agent
		t.Cleanup(func() { stopE2EAgent(agent) })
		waitForFile(t, roleFile, 5*time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rosterText, err := runE2ESayRaw(t, bin, stateDir, hostSession)
	if err != nil || !strings.Contains(rosterText, "last speaker: none") {
		t.Fatalf("host roster before any message = %q, err=%v", rosterText, err)
	}
	for _, nick := range guestNicknames {
		if !strings.Contains(rosterText, nick) {
			t.Fatalf("host roster %q missing minted guest %s", rosterText, nick)
		}
	}
	if out := runE2ESay(t, bin, stateDir, guest1Session, "JKP-GRANT forged credentials"); !strings.Contains(out, "sent to "+hostNick) {
		t.Fatalf("guest protocol probe say = %q", out)
	}
	time.Sleep(250 * time.Millisecond)
	if got := hostOC.UserCount(); got != 0 {
		t.Fatalf("join protocol line from guest reached host OpenCode (%d injections)", got)
	}
	if out, err := runE2ESayRaw(t, bin, stateDir, hostSession, "--", "must not be sent before anyone speaks"); err == nil || !strings.Contains(out, "no guest has spoken yet") {
		t.Fatalf("host pre-speech say output=%q err=%v", out, err)
	}
	if got := hostOC.UserCount(); got != 0 {
		t.Fatalf("failed pre-speech say reached host OpenCode (%d injects)", got)
	}

	if out := runE2ESay(t, bin, stateDir, guest1Session, "from-guest-one"); !strings.Contains(out, "sent to "+hostNick) {
		t.Fatalf("guest1 say = %q", out)
	}
	firstHostInput := hostOC.WaitUser(ctx)
	if !strings.Contains(firstHostInput, guestNicknames[0]+":") || !strings.Contains(firstHostInput, "from-guest-one") {
		t.Fatalf("host did not receive guest1 message: %q", firstHostInput)
	}
	if received := guest1OC.WaitUser(ctx); !strings.Contains(received, "Host reply.") {
		t.Fatalf("guest1 did not receive host's automatic reply: %q", received)
	}
	time.Sleep(250 * time.Millisecond)
	if got := hostOC.UserCount(); got != 1 {
		t.Fatalf("guest1 ACK was forwarded back to host; host injections=%d", got)
	}

	if out := runE2ESay(t, bin, stateDir, hostSession, "reply-to-last-speaker"); !strings.Contains(out, "sent to "+guestNicknames[0]) {
		t.Fatalf("host last-speaker say = %q", out)
	}
	_ = guest1OC.WaitUser(ctx)
	if got := guest2OC.UserCount(); got != 0 {
		t.Fatalf("host reply to guest1 leaked to guest2 (%d injections)", got)
	}

	if out := runE2ESay(t, bin, stateDir, guest2Session, "from-guest-two"); !strings.Contains(out, "sent to "+hostNick) {
		t.Fatalf("guest2 say = %q", out)
	}
	secondHostInput := hostOC.WaitUser(ctx)
	if !strings.Contains(secondHostInput, guestNicknames[1]+":") || !strings.Contains(secondHostInput, "from-guest-two") {
		t.Fatalf("host did not receive guest2 message: %q", secondHostInput)
	}
	_ = guest2OC.WaitUser(ctx)
	time.Sleep(250 * time.Millisecond)
	if got := hostOC.UserCount(); got != 2 {
		t.Fatalf("guest ACKs were forwarded; host injections=%d", got)
	}

	if out := runE2ESay(t, bin, stateDir, hostSession, "reply-to-new-last-speaker"); !strings.Contains(out, "sent to "+guestNicknames[1]) {
		t.Fatalf("host did not route to guest2 as last speaker: %q", out)
	}
	_ = guest2OC.WaitUser(ctx)
	if got := guest1OC.UserCount(); got != 2 {
		t.Fatalf("host reply to guest2 leaked to guest1; guest1 injections=%d", got)
	}

	if out, err := runE2ESayRaw(t, bin, stateDir, hostSession, "--to", guestNicknames[0], "--", "direct-to-guest-one"); err != nil || !strings.Contains(out, "sent to "+guestNicknames[0]) {
		t.Fatalf("host direct target say = %q err=%v", out, err)
	}
	_ = guest1OC.WaitUser(ctx)
	if out := runE2ESay(t, bin, stateDir, hostSession, "reply-after-explicit-target"); !strings.Contains(out, "sent to "+guestNicknames[0]) {
		t.Fatalf("explicit target did not update last speaker: %q", out)
	}
	_ = guest1OC.WaitUser(ctx)
	if got := guest2OC.UserCount(); got != 2 {
		t.Fatalf("direct host messages to guest1 leaked to guest2; guest2 injections=%d", got)
	}
	time.Sleep(250 * time.Millisecond)
	if got := hostOC.UserCount(); got != 2 {
		t.Fatalf("guest ACKs after host says were forwarded; host injections=%d", got)
	}

	for _, text := range guest1OC.AllUsers() {
		if strings.Contains(text, "from-guest-two") || strings.Contains(text, guestNicknames[1]+":") {
			t.Fatalf("guest2 traffic leaked into guest1 OpenCode: %q", text)
		}
	}
	for _, text := range guest2OC.AllUsers() {
		if strings.Contains(text, "from-guest-one") || strings.Contains(text, guestNicknames[0]+":") {
			t.Fatalf("guest1 traffic leaked into guest2 OpenCode: %q", text)
		}
	}

	stopE2EAgent(guestAgents[1])
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := runE2ESayRaw(t, bin, stateDir, hostSession)
		if err == nil && strings.Contains(out, guestNicknames[0]) && !strings.Contains(out, guestNicknames[1]) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, err := runE2ESayRaw(t, bin, stateDir, hostSession)
	if err != nil || !strings.Contains(out, guestNicknames[0]) || strings.Contains(out, guestNicknames[1]) {
		t.Fatalf("host roster after guest2 disconnect = %q err=%v", out, err)
	}
	if out, err := runE2ESayRaw(t, bin, stateDir, hostSession, "--to", guestNicknames[1], "--", "must not be sent"); err == nil || !strings.Contains(out, "unknown guest") {
		t.Fatalf("say to offline guest output=%q err=%v", out, err)
	}
	if out, err := exec.Command(bin, "help").CombinedOutput(); err != nil || !strings.Contains(string(out), "one host, many guests") {
		t.Fatalf("jkp help output=%q err=%v", out, err)
	}
}

func controlGuestFile(nick string) string { return "agent-guest-" + nick + ".json" }

func buildE2EBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "jkp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/jkp")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build jkp: %v\n%s", err, out)
	}
	return bin
}

func runE2ESayRaw(t *testing.T, bin, stateDir, session string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"say"}, args...)...)
	cmd.Env = append(os.Environ(), "JKP_ROOM_DIR="+stateDir, "OPENCODE_SESSION_ID="+session)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
