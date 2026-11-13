package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joey/jkp/internal/control"
)

func TestRemoveRoomFilesRemovesClaudeSidecarStateFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"claude-g1.json", "claude-g2.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeRoomFiles(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-g1.json", "claude-g2.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still exists or stat error: %v", name, err)
		}
	}
}

func TestRemoveRoomFilesRemovesEveryGuestControlFile(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "room.json"),
		filepath.Join(dir, control.HostFileName),
		filepath.Join(dir, "agent-guest-g1.json"),
		filepath.Join(dir, "agent-guest-g2.json"),
		filepath.Join(dir, control.GuestFileName),
		filepath.Join(dir, control.LegacyFileName),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeRoomFiles(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists or stat error: %v", filepath.Base(path), err)
		}
	}
}
