package control_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/control"
)

func TestCleanLegacyFilesRemovesOldControlFilesAndPreservesLiveV2Agents(t *testing.T) {
	dir := t.TempDir()
	for name, info := range map[string]control.Info{
		"agent-host.json":      {PID: os.Getpid(), Role: "host", Nick: "host", SessionID: "s-host"},
		"agent-guest-g1.json":  {PID: os.Getpid(), Role: "guest", Nick: "g1", Peer: "host", SessionID: "s-g1"},
		control.LegacyFileName: {PID: os.Getpid()},
		control.GuestFileName:  {PID: os.Getpid()},
	} {
		info.Addr = "http://127.0.0.1:1"
		info.Token = "test-token"
		data, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		// Legacy metadata need not satisfy the current control-file schema.
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cleaned, err := control.CleanLegacyFiles(dir)
	if err != nil || !cleaned {
		t.Fatalf("CleanLegacyFiles = (%t, %v), want (true, nil)", cleaned, err)
	}
	for _, name := range []string{control.LegacyFileName, control.GuestFileName} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("legacy file %s still exists or stat failed: %v", name, err)
		}
	}
	infos, err := control.LoadAgents(dir)
	if err != nil || len(infos) != 2 {
		t.Fatalf("LoadAgents = (%v, %v), want 2 live agents", infos, err)
	}
}

func TestCleanLegacyFilesStopsRecordedLegacyProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep is unavailable on Windows")
	}
	dir := t.TempDir()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waited
	})
	path := filepath.Join(dir, control.GuestFileName)
	data, err := json.Marshal(control.Info{PID: cmd.Process.Pid})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cleaned, err := control.CleanLegacyFiles(dir)
	if err != nil || !cleaned {
		t.Fatalf("CleanLegacyFiles = (%t, %v), want (true, nil)", cleaned, err)
	}
	select {
	case err := <-waited:
		// Leave the result available for cleanup without calling Wait twice.
		waited <- err
	case <-time.After(2 * time.Second):
		t.Fatal("legacy process did not exit within 2 seconds")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("legacy control file still exists or stat failed: %v", err)
	}
}

func TestSessionConflictWhenAnotherRoleOwnsSession(t *testing.T) {
	dir := t.TempDir()
	path, err := control.Path(dir, "host")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(path, control.Info{
		Addr: "http://127.0.0.1:1", Token: "secret", Role: "host", Nick: "host", Peer: "guest",
		PID: os.Getpid(), SessionID: "ses_shared", OpenCodeURL: "http://127.0.0.1:4200",
	}); err != nil {
		t.Fatal(err)
	}
	err = control.SessionConflict(dir, "ses_shared", "guest")
	if err == nil || !strings.Contains(err.Error(), "already bound as host") {
		t.Fatalf("got %v", err)
	}
	if err := control.SessionConflict(dir, "ses_shared", "host"); err != nil {
		t.Fatalf("same role should be allowed: %v", err)
	}
}

func TestSessionConflictAllowsSameNickAndRejectsDifferentGuestNick(t *testing.T) {
	dir := t.TempDir()
	path, err := control.Path(dir, "guest", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Save(path, control.Info{
		Addr: "http://127.0.0.1:1", Token: "secret", Role: "guest", Nick: "g1", Peer: "host",
		PID: os.Getpid(), SessionID: "ses_shared",
	}); err != nil {
		t.Fatal(err)
	}
	if err := control.SessionConflict(dir, "ses_shared", "g1"); err != nil {
		t.Fatalf("same guest identity should be reusable: %v", err)
	}
	if err := control.SessionConflict(dir, "ses_shared", "g2"); err == nil || !strings.Contains(err.Error(), "already bound as g1") {
		t.Fatalf("different guest identity conflict = %v", err)
	}
}
