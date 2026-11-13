package host

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedEntrypointMatchesComposeEntrypoint(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "deploy", "ergo", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ergoEntrypoint, onDisk) {
		t.Fatal("embedded Ergo entrypoint differs from deploy/ergo/entrypoint.sh")
	}
}

func TestEnsureErgoRunsEmbeddedServerWithoutCompose(t *testing.T) {
	cacheDir := t.TempDir()
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "ps" {
			return nil, nil
		}
		return nil, nil
	}
	if err := ensureErgoWith(cacheDir, run, func() error { return nil }, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1][0] != "run" {
		t.Fatalf("docker calls = %#v, want status check then docker run", calls)
	}
	joined := strings.Join(calls[1], " ")
	for _, want := range []string{"--name jkp-ergo", "ghcr.io/ergochat/ergo:stable", "127.0.0.1:6667:6667", "0.0.0.0:6697:6697", "jkp-ergo-data:/ircd", "/setup/entrypoint.sh"} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "compose") {
		t.Fatalf("docker run must not depend on compose: %q", joined)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "entrypoint.sh")); err != nil {
		t.Fatalf("embedded entrypoint not materialized: %v", err)
	}
}

func TestEnsureErgoStartsExistingStoppedContainer(t *testing.T) {
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "ps" {
			return []byte("exited\n"), nil
		}
		return nil, nil
	}
	if err := ensureErgoWith(t.TempDir(), run, func() error { return nil }, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || strings.Join(calls[1], " ") != "start jkp-ergo" {
		t.Fatalf("docker calls = %#v, want status check then docker start", calls)
	}
}
