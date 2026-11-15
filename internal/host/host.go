// Package host runs the `jkp host` command: start Ergo, register accounts,
// bind the current OpenCode session, spawn the agent, and print an invite.
package host

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/ergoacct"
	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/room"
)

// Deps holds all external effects so tests can stub them.
type Deps struct {
	RoomDir          string
	CleanedLegacy    bool
	LANIP            func() (string, error)
	EnsureErgo       func() error
	Fingerprint      func() (string, error)
	Register         func(nick, pass string) error
	StartAgent       func(args []string) (int, error)
	AgentAlive       func(pid int) bool
	OpenCodeURL      string
	OpenCodePassword string
	OpenCodeSession  string
}

// Host orchestrates room setup and invite printing.
type Host struct {
	deps Deps
}

// New returns a Host using deps.
func New(deps Deps) *Host {
	return &Host{deps: deps}
}

// Run sets up the room and prints the invite. Safe to call multiple times.
func (h *Host) Run(out io.Writer) error {
	if h.deps.CleanedLegacy {
		fmt.Fprintln(out, "jkp: cleared leftover room from an older jkp")
	}
	if err := h.deps.EnsureErgo(); err != nil {
		return fmt.Errorf("start Ergo: %w", err)
	}

	lanIP, err := h.deps.LANIP()
	if err != nil {
		return err
	}
	if lanIP == "" {
		return fmt.Errorf("no routable LAN address; pass --lan-ip")
	}

	fp, err := h.deps.Fingerprint()
	if err != nil {
		return fmt.Errorf("TLS certificate fingerprint: %w", err)
	}

	r, err := h.ensureRoom(fp, out)
	if err != nil {
		return err
	}

	// Update fingerprint if it changed (container recreated).
	if r.FP != fp {
		if err := room.SaveFP(h.deps.RoomDir, fp); err != nil {
			return err
		}
		r.FP = fp
	}

	if !h.deps.AgentAlive(r.AgentPID) {
		args := h.agentArgs(r)
		pid, err := h.deps.StartAgent(args)
		if err != nil {
			return fmt.Errorf("start agent: %w", err)
		}
		if err := room.SavePID(h.deps.RoomDir, pid); err != nil {
			return err
		}
		r.AgentPID = pid
	}

	tok, err := invite.Encode(invite.Invite{
		Host:         lanIP,
		Port:         6697,
		JoinNick:     r.JoinNick,
		JoinPassword: r.JoinPassword,
		Peer:         r.HostNick,
		FP:           fp,
	})
	if err != nil {
		return fmt.Errorf("encode invite: %w", err)
	}

	fmt.Fprintf(out, "jkp: room ready. You are %s. Guests join with this invite.\n", r.HostNick)
	fmt.Fprintf(out, "jkp: host %s:6697 (TLS)\n", lanIP)
	fmt.Fprintf(out, "jkp: bound OpenCode session %s\n", h.deps.OpenCodeSession)
	fmt.Fprintf(out, "\nINVITE %s\n\n", tok)
	fmt.Fprintf(out, "Send this same invite to every guest. In OpenCode or pi (or Claude Code), run:\n\n")
	fmt.Fprintf(out, "  /jkp %s\n\n", tok)
	fmt.Fprintf(out, "If their client cannot connect, allow incoming TCP 6697 on this Mac.\n")
	return nil
}

// ensureRoom loads the room or creates a new one with fresh passwords.
func (h *Host) ensureRoom(fp string, out io.Writer) (room.Room, error) {
	r, err := room.Load(h.deps.RoomDir)
	if err == nil {
		if isCurrentRoom(r) {
			return r, nil
		}
		if _, err := cleanLegacyRoom(h.deps.RoomDir); err != nil {
			return room.Room{}, err
		}
		if !h.deps.CleanedLegacy {
			fmt.Fprintln(out, "jkp: cleared leftover room from an older jkp")
		}
	}
	// Room doesn't exist; create it.
	hostPW, err := randomPassword()
	if err != nil {
		return room.Room{}, err
	}
	joinPW, err := randomPassword()
	if err != nil {
		return room.Room{}, err
	}
	if err := h.deps.Register("host", hostPW); err != nil {
		return room.Room{}, fmt.Errorf("register host: %w", err)
	}
	if err := h.deps.Register("join", joinPW); err != nil {
		return room.Room{}, fmt.Errorf("register guest join account: %w", err)
	}
	return room.Create(h.deps.RoomDir, room.Material{
		HostPassword: hostPW,
		JoinPassword: joinPW,
		FP:           fp,
	})
}

func isCurrentRoom(r room.Room) bool {
	return r.HostNick == "host" && r.JoinNick == "join" && r.HostPassword != "" && r.JoinPassword != ""
}

func cleanLegacyRoom(roomDir string) (bool, error) {
	cleaned, err := control.CleanLegacyFiles(roomDir)
	if err != nil {
		return cleaned, err
	}
	if r, loadErr := room.Load(roomDir); loadErr == nil && !isCurrentRoom(r) {
		if err := control.StopPID(r.AgentPID); err != nil {
			return cleaned, err
		}
		path := filepath.Join(roomDir, control.HostFileName)
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return cleaned, err
		}
		if err == nil {
			var info struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(data, &info) == nil {
				if err := control.StopPID(info.PID); err != nil {
					return cleaned, err
				}
			}
			if err := os.Remove(path); err != nil {
				return cleaned, err
			}
		}
		if err := os.Remove(filepath.Join(roomDir, "room.json")); err != nil {
			return cleaned, err
		}
		cleaned = true
	}
	return cleaned, nil
}

// agentArgs returns the CLI args for the host agent process.
func (h *Host) agentArgs(r room.Room) []string {
	args := []string{
		"agent",
		"--name", r.HostNick,
		"--harness", "opencode",
		"--oc-url", h.deps.OpenCodeURL,
		"--oc-user", "opencode",
		"--oc-password", h.deps.OpenCodePassword,
		"--oc-session", h.deps.OpenCodeSession,
		"--irc-host", "127.0.0.1",
		"--irc-port", "6667",
		"--irc-password", r.HostPassword,
		"--control-file", filepath.Join(h.deps.RoomDir, control.HostFileName),
		"--control-role", "host",
		"--control-log", filepath.Join(os.TempDir(), "jkp-host-agent.log"),
		"--control-session", h.deps.OpenCodeSession,
		"--mint-guests", "--join-nick", r.JoinNick,
	}
	return args
}

func randomPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RealDeps returns production dependencies that shell out to Docker and
// generate real credentials. roomDir overrides the default config dir.
func RealDeps(roomDir, lanIPOverride, ocSessionOverride string) (Deps, error) {
	if roomDir == "" {
		d, err := room.Dir()
		if err != nil {
			return Deps{}, err
		}
		roomDir = d
	}
	cleaned, err := cleanLegacyRoom(roomDir)
	if err != nil {
		return Deps{}, err
	}
	getenv := os.Getenv
	if ocSessionOverride != "" {
		getenv = func(key string) string {
			if key == "JKP_OC_SESSION" {
				return ocSessionOverride
			}
			return os.Getenv(key)
		}
	}
	paneURL, paneSession, unservedTUI := PaneOpenCodeBindingWith(getenv, processCommandByPID)
	if unservedTUI {
		return Deps{}, fmt.Errorf("this OpenCode window has no server for jkp to refresh; restart it with 'opencode --port 4096' (or another free port), 'opencode attach <url>', or ocmux")
	}
	ocURL, ocSession, reusable := reusableHostBinding(roomDir)
	if reusable {
		cwd, _ := os.Getwd()
		if err := validateReusableHostBindingForPane(control.Info{OpenCodeURL: ocURL, SessionID: ocSession}, paneURL, paneSession, cwd, getenv("OPENCODE_SERVER_PASSWORD")); err != nil {
			return Deps{}, err
		}
	}
	if !reusable {
		var err error
		ocURL, ocSession, err = DiscoverOpenCodeIn(getenv, roomDir)
		if err != nil {
			return Deps{}, err
		}
	}
	if err := control.SessionConflict(roomDir, ocSession, "host"); err != nil {
		return Deps{}, err
	}
	return Deps{
		RoomDir:       roomDir,
		CleanedLegacy: cleaned,
		LANIP: func() (string, error) {
			if lanIPOverride != "" {
				return lanIPOverride, nil
			}
			return detectLANIP()
		},
		EnsureErgo:  ensureErgo,
		Fingerprint: fingerprintFromErgo,
		Register: func(nick, pass string) error {
			return (ergoacct.Registrar{}).Register(nick, pass)
		},
		StartAgent:       startAgent,
		AgentAlive:       agentAlive,
		OpenCodeURL:      ocURL,
		OpenCodePassword: os.Getenv("OPENCODE_SERVER_PASSWORD"),
		OpenCodeSession:  ocSession,
	}, nil
}

// detectLANIP shells out to find the routable address.
func detectLANIP() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", fmt.Errorf("detect LAN IP: %w", err)
	}
	var iface string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "interface:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				iface = fields[len(fields)-1]
			}
		}
	}
	if iface == "" {
		return "", fmt.Errorf("cannot determine default interface")
	}
	ipOut, err := exec.Command("ipconfig", "getifaddr", iface).Output()
	if err != nil {
		return "", fmt.Errorf("getifaddr %s: %w", iface, err)
	}
	ip := strings.TrimSpace(string(ipOut))
	if ip == "" {
		return "", fmt.Errorf("interface %s has no IPv4", iface)
	}
	return ip, nil
}

//go:embed entrypoint.sh
var ergoEntrypoint []byte

type dockerRunner func(args ...string) ([]byte, error)

// ensureErgo starts the Ergo container without depending on the current directory.
func ensureErgo() error {
	dir, err := room.Dir()
	if err != nil {
		return fmt.Errorf("Ergo cache directory: %w", err)
	}
	return ensureErgoWith(dir, dockerCommand, dialLoopback, time.Sleep)
}

func ensureErgoWith(cacheDir string, run dockerRunner, dial func() error, sleep func(time.Duration)) error {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("create Ergo cache directory: %w", err)
	}
	entrypoint := filepath.Join(cacheDir, "entrypoint.sh")
	if err := os.WriteFile(entrypoint, ergoEntrypoint, 0o644); err != nil {
		return fmt.Errorf("write embedded Ergo entrypoint: %w", err)
	}

	state, err := run("ps", "-a", "--filter", "name=^/jkp-ergo$", "--format", "{{.State}}")
	if err != nil {
		return fmt.Errorf("Docker is unavailable; start Docker and try again: %w", err)
	}
	state = []byte(strings.TrimSpace(string(state)))
	switch string(state) {
	case "running":
		return waitForErgo(dial, sleep)
	case "exited", "created":
		if _, err := run("start", "jkp-ergo"); err != nil {
			return fmt.Errorf("start existing Ergo container: %w", err)
		}
	default:
		if len(state) != 0 {
			// Docker may report restarting or paused; let the readiness loop
			// determine whether it can serve IRC rather than issuing another start.
			return waitForErgo(dial, sleep)
		}
		mount := "type=bind,source=" + entrypoint + ",target=/setup/entrypoint.sh,readonly"
		args := []string{
			"run", "-d", "--name", "jkp-ergo",
			"-p", "127.0.0.1:6667:6667",
			"-p", "0.0.0.0:6697:6697",
			"-v", "jkp-ergo-data:/ircd",
			"--mount", mount,
			"--entrypoint", "/bin/sh",
			"ghcr.io/ergochat/ergo:stable", "/setup/entrypoint.sh",
		}
		if _, err := run(args...); err != nil {
			return fmt.Errorf("start Ergo container: %w", err)
		}
	}
	return waitForErgo(dial, sleep)
}

func waitForErgo(dial func() error, sleep func(time.Duration)) error {
	for i := 0; i < 30; i++ {
		if err := dial(); err == nil {
			return nil
		}
		sleep(time.Second)
	}
	return fmt.Errorf("Ergo did not become ready within 30s")
}

func dockerCommand(args ...string) ([]byte, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func dialLoopback() error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:6667", 2*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// fingerprintFromErgo extracts the TLS cert fingerprint from the running container.
func fingerprintFromErgo() (string, error) {
	out, err := exec.Command("docker", "exec", "jkp-ergo", "cat", "/ircd/fullchain.pem").Output()
	if err != nil {
		return "", fmt.Errorf("read TLS cert: %w", err)
	}
	return irc.FingerprintPEM(out)
}

// startAgent spawns the jkp agent as a detached child process.
func startAgent(args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, args...)
	logFile := filepath.Join(os.TempDir(), fmt.Sprintf("jkp-%s.log", args[2])) // args[2] is --name value... actually it's after "agent" "--name" "oc"
	// Safer: just use a fixed log name based on harness
	logFile = filepath.Join(os.TempDir(), "jkp-host-agent.log")
	lf, err := os.Create(logFile)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// Don't wait; let it run independently.
	go cmd.Wait()
	return cmd.Process.Pid, nil
}

// agentAlive checks if a pid is still running.
func agentAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Signal 0 checks existence.
	return proc.Signal(syscall.Signal(0)) == nil
}
