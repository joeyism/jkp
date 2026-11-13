package claudeadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/claudeadapter"
	"github.com/joey/jkp/internal/jkpclient"
)

const (
	tuiSession     = "11111111-1111-4111-8111-111111111111"
	sidecarSession = "22222222-2222-4222-8222-222222222222"
)

type fakeTurn struct {
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	Stdout    *string `json:"stdout,omitempty"`
	IsError   bool    `json:"is_error"`
	ExitCode  int     `json:"exit_code"`
	Sleep     float64 `json:"sleep"`
}

type invocation struct {
	Argv          []string `json:"argv"`
	Cwd           string   `json:"cwd"`
	ClaudeCodeEnv string   `json:"claude_code_env"`
}

type fakeClaude struct {
	bin string
	dir string
}

// Each fake has its own call counter and configuration; no global environment
// changes are needed. exec replaces the shell so cancellation kills the sleeping
// Python process itself, rather than leaving a child holding stdout open.
func writeFakeClaude(t *testing.T, turns ...fakeTurn) fakeClaude {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is POSIX-only")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("fake claude requires python3")
	}
	dir := t.TempDir()
	for i := range turns {
		if turns[i].SessionID == "" {
			turns[i].SessionID = sidecarSession
		}
	}
	config, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "turns.json"), config, 0o600)
	script := `import json, os, pathlib, sys, time
root = pathlib.Path(__file__).parent
turns = json.loads((root / "turns.json").read_text())
index = len(list(root.glob("call-*.json")))
(root / ("call-%d.json" % (index + 1))).write_text(json.dumps({"argv": sys.argv[1:], "cwd": os.getcwd(), "claude_code_env": os.environ.get("CLAUDECODE", "")}))
if index >= len(turns):
    sys.stderr.write("unexpected extra fake claude invocation\n")
    sys.exit(99)
turn = turns[index]
time.sleep(turn["sleep"])
if "stdout" in turn:
    sys.stdout.write(turn["stdout"])
else:
    print(json.dumps({"type": "result", "subtype": "error_during_execution" if turn["is_error"] else "success", "is_error": turn["is_error"], "session_id": turn["session_id"], "result": turn["result"]}))
sys.exit(turn["exit_code"])
`
	pyPath := filepath.Join(dir, "fake.py")
	writeTestFile(t, pyPath, []byte(script), 0o600)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	bin := filepath.Join(dir, "claude")
	writeTestFile(t, bin, []byte("#!/bin/sh\ntouch "+quote(filepath.Join(dir, "started"))+"\nexec "+quote(python)+" "+quote(pyPath)+" \"$@\"\n"), 0o755)
	return fakeClaude{bin: bin, dir: dir}
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func (f fakeClaude) call(t *testing.T, turn int) invocation {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("call-%d.json", turn)))
	if err != nil {
		t.Fatal(err)
	}
	var call invocation
	if err := json.Unmarshal(b, &call); err != nil {
		t.Fatal(err)
	}
	return call
}

func configFor(t *testing.T, f fakeClaude) claudeadapter.Config {
	t.Helper()
	return claudeadapter.Config{
		ClaudeBinary: f.bin,
		ForkFrom:     tuiSession,
		StateFile:    filepath.Join(t.TempDir(), "session.json"),
		EndpointID:   "claude-agent",
		TurnTimeout:  3 * time.Second,
	}
}

func message() jkpclient.Event {
	ev := jkpclient.Event{ID: "evt-1", Kind: "dm", Target: "claude-agent", Body: "why is the sky blue?"}
	ev.Sender.Nick = "alice"
	return ev
}

func handle(t *testing.T, a *claudeadapter.Adapter, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	got, err := a.Handle(ctx, message())
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got != want {
		t.Fatalf("Handle returned %q, want %q", got, want)
	}
}

func argIndex(argv []string, want string) int {
	for i, arg := range argv {
		if arg == want {
			return i
		}
	}
	return -1
}

func requireArg(t *testing.T, argv []string, want string) {
	t.Helper()
	if argIndex(argv, want) < 0 {
		t.Fatalf("argv %q missing %q", argv, want)
	}
}

func rejectArg(t *testing.T, argv []string, unwanted string) {
	t.Helper()
	if argIndex(argv, unwanted) >= 0 {
		t.Fatalf("argv %q unexpectedly contains %q", argv, unwanted)
	}
}

func flagValue(t *testing.T, argv []string, flag string) string {
	t.Helper()
	i := argIndex(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		t.Fatalf("argv %q missing value for %s", argv, flag)
	}
	return argv[i+1]
}

func requireFlagValue(t *testing.T, argv []string, flag, want string) {
	t.Helper()
	if got := flagValue(t, argv, flag); got != want {
		t.Fatalf("%s = %q, want %q; argv %q", flag, got, want, argv)
	}
}

func requireSidecarResume(t *testing.T, argv []string) {
	t.Helper()
	requireFlagValue(t, argv, "--resume", sidecarSession)
	rejectArg(t, argv, "--fork-session")
	rejectArg(t, argv, "--session-id")
	rejectArg(t, argv, tuiSession)
}

func TestFirstTurnForksFromTUISessionAndPersistsReturnedID(t *testing.T) {
	f := writeFakeClaude(t, fakeTurn{Result: "first answer"})
	cfg := configFor(t, f)
	handle(t, claudeadapter.New(cfg), "first answer")
	argv := f.call(t, 1).Argv
	requireArg(t, argv, "-p")
	requireFlagValue(t, argv, "--resume", tuiSession)
	requireArg(t, argv, "--fork-session")
	requireFlagValue(t, argv, "--output-format", "json")
	prompt := flagValue(t, argv, "--")
	if !strings.Contains(prompt, message().Sender.Nick) || !strings.Contains(prompt, message().Body) {
		t.Fatalf("prompt must attribute sender and carry body: %q", prompt)
	}
	if !strings.Contains(prompt, jkpclient.LocalOnlyMarker) || !strings.Contains(prompt, "START") || !strings.Contains(prompt, "16384") {
		t.Fatalf("prompt lacks automatic-reply guidance: %q", prompt)
	}
	b, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	if state.SessionID != sidecarSession {
		t.Fatalf("persisted session = %q, want %q", state.SessionID, sidecarSession)
	}
	info, err := os.Stat(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSecondTurnResumesSidecarWithoutForkAndNeverTUISession(t *testing.T) {
	f := writeFakeClaude(t, fakeTurn{Result: "first answer"}, fakeTurn{Result: "second answer"})
	a := claudeadapter.New(configFor(t, f))
	handle(t, a, "first answer")
	handle(t, a, "second answer")
	requireSidecarResume(t, f.call(t, 2).Argv)
}

func TestRestartWithExistingStateFileResumesWithoutReforking(t *testing.T) {
	f := writeFakeClaude(t, fakeTurn{Result: "first answer"}, fakeTurn{Result: "after restart"})
	cfg := configFor(t, f)
	handle(t, claudeadapter.New(cfg), "first answer")
	handle(t, claudeadapter.New(cfg), "after restart")
	requireSidecarResume(t, f.call(t, 2).Argv)
}

func TestMissingOrUnexpandedForkFromStartsFreshUUIDSessionAndWarnsOnce(t *testing.T) {
	for _, forkFrom := range []string{"", "${CLAUDE_SESSION_ID}"} {
		t.Run(fmt.Sprintf("ForkFrom=%q", forkFrom), func(t *testing.T) {
			f := writeFakeClaude(t, fakeTurn{Result: "first"}, fakeTurn{Result: "second"})
			cfg := configFor(t, f)
			var warnings bytes.Buffer
			cfg.ForkFrom, cfg.Warn = forkFrom, &warnings
			a := claudeadapter.New(cfg)
			handle(t, a, "first")
			argv := f.call(t, 1).Argv
			id := flagValue(t, argv, "--session-id")
			if !regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
				t.Fatalf("fresh session ID is not a UUIDv4: %q", id)
			}
			rejectArg(t, argv, "--fork-session")
			rejectArg(t, argv, "--resume")
			handle(t, a, "second")
			requireSidecarResume(t, f.call(t, 2).Argv)
			warning := strings.TrimSpace(warnings.String())
			if warning == "" || len(strings.Split(warning, "\n")) != 1 {
				t.Fatalf("want exactly one warning line across two turns, got %q", warnings.String())
			}
		})
	}
}

func TestIsolationFlagsAlwaysPresentAndModelOptional(t *testing.T) {
	for _, model := range []string{"", "haiku"} {
		t.Run("model="+model, func(t *testing.T) {
			f := writeFakeClaude(t, fakeTurn{Result: "first"}, fakeTurn{Result: "second"})
			cfg := configFor(t, f)
			cfg.Model = model
			a := claudeadapter.New(cfg)
			for i, reply := range []string{"first", "second"} {
				handle(t, a, reply)
				argv := f.call(t, i+1).Argv
				requireFlagValue(t, argv, "--tools", "")
				requireFlagValue(t, argv, "--disallowedTools", "mcp__*")
				requireArg(t, argv, "--strict-mcp-config")
				requireArg(t, argv, "--safe-mode")
				if model == "" {
					rejectArg(t, argv, "--model")
				} else {
					requireFlagValue(t, argv, "--model", model)
				}
			}
		})
	}
}

func TestStripsNestedClaudeCodeEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	f := writeFakeClaude(t, fakeTurn{Result: "ok"})
	handle(t, claudeadapter.New(configFor(t, f)), "ok")
	call := f.call(t, 1)
	if call.ClaudeCodeEnv != "" {
		t.Fatalf("CLAUDECODE = %q, want empty", call.ClaudeCodeEnv)
	}
}

func TestProjectDirSetsWorkingDirectory(t *testing.T) {
	f := writeFakeClaude(t, fakeTurn{Result: "ok"})
	projDir := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configFor(t, f)
	cfg.ProjectDir = projDir
	handle(t, claudeadapter.New(cfg), "ok")
	if got := f.call(t, 1).Cwd; got != resolved {
		t.Fatalf("cwd = %q, want %q", got, resolved)
	}
}

func TestMultilineReplyNotTruncatedAndErrorsRejected(t *testing.T) {
	t.Run("35 lines preserved", func(t *testing.T) {
		lines := make([]string, 35)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %02d", i+1)
		}
		reply := strings.Join(lines, "\n")
		f := writeFakeClaude(t, fakeTurn{Result: reply})
		handle(t, claudeadapter.New(configFor(t, f)), reply)
	})
	notJSON := "not JSON\n"
	for _, tc := range []struct {
		name string
		turn fakeTurn
	}{
		{"is_error", fakeTurn{Result: "failed", IsError: true}},
		{"empty result", fakeTurn{Result: ""}},
		{"whitespace result", fakeTurn{Result: " \n\t "}},
		{"non-JSON stdout", fakeTurn{Stdout: &notJSON}},
		{"nonzero exit", fakeTurn{Result: "looks successful", ExitCode: 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := writeFakeClaude(t, tc.turn)
			a := claudeadapter.New(configFor(t, f))
			if reply, err := a.Handle(context.Background(), message()); err == nil {
				t.Fatalf("Handle accepted invalid result: %q", reply)
			}
		})
	}
}

func TestTurnTimeoutCancelsSubprocess(t *testing.T) {
	f := writeFakeClaude(t, fakeTurn{Result: "too late", Sleep: 5})
	cfg := configFor(t, f)
	cfg.TurnTimeout = 300 * time.Millisecond
	a := claudeadapter.New(cfg)
	start := time.Now()
	_, err := a.Handle(context.Background(), message())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Handle succeeded despite turn timeout")
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("cancellation took %s, want <2s", elapsed)
	}
	text := strings.ToLower(err.Error())
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(text, "deadline") && !strings.Contains(text, "timeout") && !strings.Contains(text, "timed out") {
		t.Fatalf("want deadline/timeout error, got %v", err)
	}
	// Ensure this exercised a running subprocess rather than a preflight error.
	if _, err := os.Stat(filepath.Join(f.dir, "started")); err != nil {
		t.Fatalf("subprocess did not start: %v", err)
	}
	_ = f.call(t, 1)
}
