//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinclient"
)

// TestE2EClaudeGuest exercises the Claude sidecar through real CLI processes
// and Ergo, without calling a model.
func TestE2EClaudeGuest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude requires POSIX")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("fake claude requires python3")
	}
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
	hostNick, joinNick := "e2ech"+suffix, "e2ecj"+suffix
	hostPass, joinPass := "host-"+suffix, "join-"+suffix
	// registerTestAccount installs cleanup for both registered accounts.
	registerTestAccount(t, ergo, hostNick, hostPass)
	registerTestAccount(t, ergo, joinNick, joinPass)

	const (
		hostSession    = "ses-e2e-claude-host"
		tuiSessionUUID = "11111111-1111-4111-8111-111111111111"
		sidecarUUID    = "22222222-2222-4222-8222-222222222222"
		codewordReply  = "The secret codeword is cobalt-falcon."
	)
	longReply := strings.TrimSpace(strings.Repeat("Claude sidecar preserves long chunked replies end to end. ", 50))
	fakeDir := t.TempDir()
	writeFile := func(path string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	turns, err := json.Marshal([]map[string]any{
		{"session_id": sidecarUUID, "result": codewordReply, "sleep": 0},
		{"session_id": sidecarUUID, "result": longReply, "sleep": 0},
		{"session_id": sidecarUUID, "result": "Acknowledged.", "sleep": 0},
		{"session_id": sidecarUUID, "result": "too late", "sleep": 3.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(fakeDir, "turns.json"), turns, 0o600)
	pyPath := filepath.Join(fakeDir, "fake.py")
	writeFile(pyPath, []byte(`import json, os, pathlib, sys, time
root = pathlib.Path(__file__).parent
turns = json.loads((root / "turns.json").read_text())
index = len(list(root.glob("call-*.json")))
call = root / ("call-%d.json" % (index + 1))
tmp = root / "call.tmp"
tmp.write_text(json.dumps({"argv": sys.argv[1:], "cwd": os.getcwd()}))
tmp.replace(call)
if index >= len(turns):
    sys.exit(99)
turn = turns[index]
time.sleep(turn["sleep"])
print(json.dumps({"type": "result", "subtype": "success", "is_error": False, "session_id": turn["session_id"], "result": turn["result"]}))
`), 0o600)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	fakeClaudeBin := filepath.Join(fakeDir, "claude")
	writeFile(fakeClaudeBin, []byte("#!/bin/sh\nexec "+quote(python)+" "+quote(pyPath)+" \"$@\"\n"), 0o755)
	projDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hostOC := newScriptedOpenCode(t, hostSession, "host-secret", func(turn int, _ string) (string, bool) {
		if turn == 1 {
			return "What is the secret codeword?", true
		}
		return "Acknowledged.", true
	})
	bin := buildE2EBinary(t)
	stateDir := t.TempDir()
	hostAgent := startE2EAgent(t, bin, stateDir, []string{
		"agent", "--name", hostNick, "--harness", "opencode",
		"--oc-url", hostOC.URL(), "--oc-password", "host-secret", "--oc-session", hostSession,
		"--irc-host", hostAddr, "--irc-port", strconv.Itoa(port), "--irc-password", hostPass,
		"--control-role", "host", "--control-file", filepath.Join(stateDir, "agent-host.json"), "--control-session", hostSession,
		"--mint-guests", "--join-nick", joinNick, "--oper-addr", ergo, "--oper-user", operUser, "--oper-pass", operPass,
	})
	t.Cleanup(func() { stopE2EAgent(hostAgent) })
	waitForFile(t, filepath.Join(stateDir, "agent-host.json"), 5*time.Second)
	grantCtx, cancelGrant := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelGrant()
	guestNick, guestPass, err := joinclient.RequestGrant(grantCtx, func(cfg irc.RealConfig) joinclient.Client {
		cfg.TLS = false // Loopback Ergo uses plaintext, not public join's pinned TLS.
		cfg.CertFP = ""
		return irc.NewRealClient(cfg)
	}, invite.Invite{Host: hostAddr, Port: port, JoinNick: joinNick, JoinPassword: joinPass, Peer: hostNick, FP: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eraseTestAccount(t, ergo, guestNick) })
	guestAgent := startE2EAgent(t, bin, stateDir, []string{
		"agent", "--name", guestNick, "--harness", "claude", "--claude-bin", fakeClaudeBin,
		"--claude-fork-from", tuiSessionUUID, "--claude-dir", projDir,
		"--claude-state-file", filepath.Join(stateDir, "claude-"+guestNick+".json"),
		"--claude-model", "haiku", "--claude-turn-timeout", "1500ms",
		"--irc-host", hostAddr, "--irc-port", strconv.Itoa(port), "--irc-password", guestPass,
		"--peer", hostNick, "--control-role", "guest",
		"--control-file", filepath.Join(stateDir, controlGuestFile(guestNick)), "--control-session", tuiSessionUUID,
		"--control-log", filepath.Join(stateDir, guestNick+".log"),
	})
	t.Cleanup(func() { stopE2EAgent(guestAgent) })
	waitForFile(t, filepath.Join(stateDir, controlGuestFile(guestNick)), 5*time.Second)
	requireAbsent := func(path string) {
		t.Helper()
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s not to exist, stat error: %v", path, err)
		}
	}
	callPath := func(turn int) string { return filepath.Join(fakeDir, fmt.Sprintf("call-%d.json", turn)) }
	readCall := func(turn int) ([]string, string) {
		t.Helper()
		data, err := os.ReadFile(callPath(turn))
		if err != nil {
			t.Fatal(err)
		}
		var call struct {
			Argv []string `json:"argv"`
			Cwd  string   `json:"cwd"`
		}
		if err := json.Unmarshal(data, &call); err != nil {
			t.Fatal(err)
		}
		return call.Argv, call.Cwd
	}
	requireFlag := func(argv []string, flag, value string) {
		t.Helper()
		i := slices.Index(argv, flag)
		if i < 0 || i+1 >= len(argv) || argv[i+1] != value {
			t.Fatalf("argv %q missing %s %q", argv, flag, value)
		}
	}
	// Explicit --session must work outside OpenCode, even if the test runner
	// inherited OPENCODE_SESSION_ID from its own environment.
	runGuestCLI := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "OPENCODE_SESSION_ID=") && !strings.HasPrefix(env, "JKP_ROOM_DIR=") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, "JKP_ROOM_DIR="+stateDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("jkp %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if got := hostOC.UserCount(); got != 0 {
		t.Fatalf("silent join injected %d host turns", got)
	}
	requireAbsent(callPath(1))
	if out := runGuestCLI("say", "--session", tuiSessionUUID, "--", "ready from claude"); !strings.Contains(out, "sent to "+hostNick+" as "+guestNick) {
		t.Fatalf("guest say output = %q", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if got := hostOC.WaitUser(ctx); !strings.Contains(got, "ready from claude") {
		t.Fatalf("host initial message = %q", got)
	}
	if got := hostOC.WaitUser(ctx); !strings.Contains(got, codewordReply) {
		for _, name := range []string{hostNick + ".log", guestNick + ".log"} {
			if data, err := os.ReadFile(filepath.Join(stateDir, name)); err == nil {
				t.Logf("%s: %s", name, data)
			}
		}
		for _, name := range []string{"call-1.json", "call-2.json", "call-3.json", "call-4.json"} {
			if data, err := os.ReadFile(filepath.Join(fakeDir, name)); err == nil {
				t.Logf("fake claude %s: %s", name, data)
			}
		}
		t.Logf("host fake inputs: %q", hostOC.AllUsers())
		t.Fatalf("host codeword reply = %q, hostCount=%d", got, hostOC.UserCount())
	}
	argv, cwd := readCall(1)
	requireFlag(argv, "--resume", tuiSessionUUID)
	requireFlag(argv, "--tools", "")
	requireFlag(argv, "--disallowedTools", "mcp__*")
	requireFlag(argv, "--model", "haiku")
	for _, flag := range []string{"--fork-session", "--strict-mcp-config", "--safe-mode"} {
		if !slices.Contains(argv, flag) {
			t.Fatalf("argv %q missing %s", argv, flag)
		}
	}
	if cwd != projDir {
		t.Fatalf("cwd = %q, want %q", cwd, projDir)
	}
	time.Sleep(250 * time.Millisecond)
	requireAbsent(callPath(2))
	time.Sleep(time.Second) // Ergo per-client flood window.
	runE2ESay(t, bin, stateDir, hostSession, "send the full report")
	if got := hostOC.WaitUser(ctx); !strings.Contains(got, longReply) {
		t.Fatalf("long reply did not arrive intact: %q", got)
	}
	argv, _ = readCall(2)
	requireFlag(argv, "--resume", sidecarUUID)
	if slices.Contains(argv, "--fork-session") || slices.Contains(argv, tuiSessionUUID) {
		t.Fatalf("second turn reforked TUI session: %q", argv)
	}
	// Initial ready + codeword + full report are the three expected host turns.
	requireHostCount := func() {
		t.Helper()
		if got := hostOC.UserCount(); got != 3 {
			t.Fatalf("host received %d turns, want 3 (ACKs and timeout errors must stay local)", got)
		}
	}
	time.Sleep(time.Second)
	runE2ESay(t, bin, stateDir, hostSession, "ack-check")
	waitForFile(t, callPath(3), 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	requireHostCount()
	time.Sleep(time.Second)
	runE2ESay(t, bin, stateDir, hostSession, "timeout-check")
	waitForFile(t, callPath(4), 5*time.Second)
	time.Sleep(2 * time.Second)
	requireHostCount()
	out := runGuestCLI("log", "--session", tuiSessionUUID)
	for _, want := range []string{"<- " + hostNick, "-> " + hostNick, "cobalt-falcon"} {
		if !strings.Contains(out, want) {
			t.Fatalf("jkp log missing %q:\n%s", want, out)
		}
	}
}
