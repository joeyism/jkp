package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StopPID lets a recorded process release its sockets before continuing.
func StopPID(pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("stop jkp agent %d: %w", pid, err)
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for IsProcessAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// CleanLegacyFiles removes only the two pre-multi-guest control files.
func CleanLegacyFiles(dir string) (bool, error) {
	cleaned := false
	for _, path := range []string{LegacyPath(dir), filepath.Join(dir, GuestFileName)} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return cleaned, err
		}
		var info struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(data, &info) == nil && info.PID > 0 {
			if err := StopPID(info.PID); err != nil {
				return cleaned, err
			}
		}
		if err := os.Remove(path); err != nil {
			return cleaned, err
		}
		cleaned = true
	}
	return cleaned, nil
}

// SessionConflict is returned when a session is already bound to another local
// agent identity.
func SessionConflict(dir, sessionID, wantNick string) error {
	if sessionID == "" {
		return nil
	}
	infos, err := LoadAgents(dir)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if info.SessionID == sessionID && info.Nick != wantNick {
			return fmt.Errorf("OpenCode session %s is already bound as %s; use a different window or run jkp reset", sessionID, info.Nick)
		}
	}
	return nil
}

// KillAgents stops recorded control-file pids and any leftover `jkp agent` processes.
func KillAgents(dir string, extraPIDs ...int) error {
	pids := map[int]struct{}{}
	for _, pid := range extraPIDs {
		if pid > 0 {
			pids[pid] = struct{}{}
		}
	}
	for _, path := range append(AgentPaths(dir), LegacyPath(dir)) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var info struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(data, &info) == nil && info.PID > 0 {
			pids[info.PID] = struct{}{}
		}
	}
	for _, pid := range listJKPAgentPIDs() {
		pids[pid] = struct{}{}
	}
	self := os.Getpid()
	for pid := range pids {
		if pid == self {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stop jkp agent %d: %w", pid, err)
		}
	}
	return nil
}

func listJKPAgentPIDs() []int {
	out, err := exec.Command("ps", "-ax", "-o", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range bytes.Split(out, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if s == "" || !strings.Contains(s, "jkp agent --name") {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}
