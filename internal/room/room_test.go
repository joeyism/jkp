package room_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/room"
)

func TestCreateThenLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r, err := room.Create(dir, room.Material{
		HostPassword: "host-pw", JoinPassword: "join-pw", FP: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.HostNick != "host" || r.JoinNick != "join" {
		t.Fatalf("nicks: %#v", r)
	}
	got, err := room.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.JoinPassword != "join-pw" || got.HostPassword != "host-pw" {
		t.Fatalf("passwords: %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "room.json"))
	if err != nil || !strings.Contains(string(data), `"join_nick": "join"`) || !strings.Contains(string(data), `"join_password": "join-pw"`) {
		t.Fatalf("room.json = %s err=%v, want join fields", data, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "room.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestCreateFailsIfExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := room.Material{HostPassword: "a", JoinPassword: "b", FP: strings.Repeat("cd", 32)}
	if _, err := room.Create(dir, m); err != nil {
		t.Fatal(err)
	}
	if _, err := room.Create(dir, m); err == nil {
		t.Fatal("second Create should fail")
	}
}

func TestLoadMissingFails(t *testing.T) {
	t.Parallel()
	if _, err := room.Load(t.TempDir()); err == nil {
		t.Fatal("Load on empty dir should fail")
	}
}

func TestDirUsesJKPRoomDirOverride(t *testing.T) {
	want := t.TempDir()
	t.Setenv("JKP_ROOM_DIR", want)
	got, err := room.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestSaveFPUpdatesFingerprint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := room.Create(dir, room.Material{
		HostPassword: "a", JoinPassword: "b", FP: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	newFP := strings.Repeat("ef", 32)
	if err := room.SaveFP(dir, newFP); err != nil {
		t.Fatal(err)
	}
	got, err := room.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.FP != newFP {
		t.Fatalf("FP %q, want %q", got.FP, newFP)
	}
	if got.HostPassword != "a" {
		t.Fatalf("HostPassword mutated: %q", got.HostPassword)
	}
}
