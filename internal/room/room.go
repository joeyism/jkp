// Package room persists the material for one LAN collaboration room: the host
// and shared guest-join SASL passwords plus the pinned TLS fingerprint. The file is created
// once by `jkp host` and reprinted on every invocation so guests can always
// paste a fresh invite. It lives in the user config directory, mode 0600.
package room

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Material holds the inputs needed to create a room.
type Material struct {
	HostPassword string
	JoinPassword string
	FP           string
}

// Room is the persisted state of one collaboration room.
type Room struct {
	HostNick     string    `json:"host_nick"`
	HostPassword string    `json:"host_password"`
	JoinNick     string    `json:"join_nick"`
	JoinPassword string    `json:"join_password"`
	FP           string    `json:"fp"`
	AgentPID     int       `json:"agent_pid"`
	Created      time.Time `json:"created"`
}

// Create writes a new room.json in dir. It fails if the file already exists.
func Create(dir string, m Material) (Room, error) {
	path := filepath.Join(dir, "room.json")
	if _, err := os.Stat(path); err == nil {
		return Room{}, fmt.Errorf("room already exists at %s", path)
	}
	r := Room{
		HostNick:     "host",
		HostPassword: m.HostPassword,
		JoinNick:     "join",
		JoinPassword: m.JoinPassword,
		FP:           m.FP,
		Created:      time.Now().UTC(),
	}
	if err := write(dir, r); err != nil {
		return Room{}, err
	}
	return r, nil
}

// Load reads room.json from dir.
func Load(dir string) (Room, error) {
	b, err := os.ReadFile(filepath.Join(dir, "room.json"))
	if err != nil {
		return Room{}, fmt.Errorf("read room: %w", err)
	}
	var r Room
	if err := json.Unmarshal(b, &r); err != nil {
		return Room{}, fmt.Errorf("parse room: %w", err)
	}
	return r, nil
}

// SaveFP updates only the fingerprint and pid, leaving passwords untouched.
func SaveFP(dir string, fp string) error {
	r, err := Load(dir)
	if err != nil {
		return err
	}
	r.FP = fp
	return write(dir, r)
}

// SavePID updates only the agent pid.
func SavePID(dir string, pid int) error {
	r, err := Load(dir)
	if err != nil {
		return err
	}
	r.AgentPID = pid
	return write(dir, r)
}

// Dir returns the default room directory: <user config>/jkp.
func Dir() (string, error) {
	if dir := os.Getenv("JKP_ROOM_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "jkp"), nil
}

func write(dir string, r Room) error {
	path := filepath.Join(dir, "room.json")
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
