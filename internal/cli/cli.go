// Package cli implements the jkp command surface. It binds the local API and
// registers agent endpoints; it never exposes raw harness control.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joey/jkp/internal/claudeadapter"
	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/ergoacct"
	"github.com/joey/jkp/internal/host"
	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinclient"
	"github.com/joey/jkp/internal/localapi"
	"github.com/joey/jkp/internal/ocadapter"
	"github.com/joey/jkp/internal/piadapter"
	"github.com/joey/jkp/internal/protocol"
	"github.com/joey/jkp/internal/room"
	"github.com/joey/jkp/internal/runner"
)

// ErrUsage signals a command-line usage problem.
var ErrUsage = errors.New("usage error")

// Version is the build version. Override at link time:
//
//	go build -ldflags "-X github.com/joey/jkp/internal/cli.Version=1.2.3"
var Version = "dev"

// CLI dispatches subcommands against a collaboration service.
type CLI struct {
	svc    *core.Service
	out    io.Writer
	errOut io.Writer
	// Env lets tests stub environment lookups.
	Env     func(string) string
	RoomDir func() (string, error)
}

// New returns a CLI writing to out and errOut.
func New(svc *core.Service, out, errOut io.Writer) *CLI {
	return &CLI{svc: svc, out: out, errOut: errOut, Env: os.Getenv, RoomDir: room.Dir}
}

// Run parses args and executes one subcommand.
func (c *CLI) Run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return c.help()
	}
	var err error
	switch args[0] {
	case "serve":
		err = c.serve(args[1:])
	case "agent":
		err = c.agent(args[1:])
	case "host":
		err = c.host(args[1:])
	case "join":
		err = c.join(args[1:])
	case "say":
		err = c.say(args[1:])
	case "log":
		err = c.logCmd(args[1:])
	case "reset":
		err = c.reset(args[1:])
	case "endpoint":
		err = c.endpoint(args[1:])
	case "version":
		err = c.version()
	default:
		err = fmt.Errorf("%w: unknown command %q", ErrUsage, args[0])
	}
	if errors.Is(err, ErrUsage) {
		return fmt.Errorf("%w (run jkp help)", err)
	}
	return err
}

func (c *CLI) help() error {
	fmt.Fprintln(c.out, "jkp commands:")
	fmt.Fprintln(c.out, "  jkp host [--oc-session ID]             start a room (OpenCode, Docker)")
	fmt.Fprintln(c.out, "  jkp join --invite <token> --harness opencode|pi|claude [--oc-session ID]")
	fmt.Fprintln(c.out, "  jkp say [--session ID] [--to <nick>] -- <text>  send a message")
	fmt.Fprintln(c.out, "  jkp say [--session ID]                 list guests and last speaker (host)")
	fmt.Fprintln(c.out, "  jkp log [--session ID] [--lines N]     show recent traffic for this pane")
	fmt.Fprintln(c.out, "  jkp reset                             clear local room state (keeps Ergo running)")
	fmt.Fprintln(c.out, "  jkp version")
	fmt.Fprintln(c.out, "")
	fmt.Fprintln(c.out, "How rooms work:")
	fmt.Fprintln(c.out, "  one host, many guests; every guest uses the same invite; guests never see each other.")
	fmt.Fprintln(c.out, "  OpenCode host/guest needs a served session: start OpenCode with opencode --port 4096, or use ocmux.")
	fmt.Fprintln(c.out, "  joining is silent. Nobody speaks until they use jkp say.")
	fmt.Fprintln(c.out, "  Host: jkp say -- <text> sends to the last guest who spoke.")
	fmt.Fprintln(c.out, "  Host: jkp say --to <nick> -- <text> sends to that guest and makes them the last speaker.")
	fmt.Fprintln(c.out, "  Host: jkp say with no text lists connected guests and the last speaker.")
	fmt.Fprintln(c.out, "  With no last speaker or an unknown nick, the message is not sent; jkp lists connected guests.")
	fmt.Fprintln(c.out, "  guest jkp say always goes to the host; no fan-out yet.")
	fmt.Fprintln(c.out, "  Agent replies are sent to the peer automatically; START a turn with [JKP-LOCAL-ONLY] to keep it local. Replies are limited to 16384 UTF-8 bytes.")
	fmt.Fprintln(c.out, "  An old invite requires jkp reset on the host, then jkp host to print a new invite.")
	fmt.Fprintln(c.out, "  Claude Code guest: joins with a tools-disabled background Claude forked from your session; use jkp log to inspect traffic.")
	fmt.Fprintln(c.out, "  Privacy: a forked Claude Code guest carries your whole conversation, so anything in that conversation can reach the host.")
	return nil
}

// agent runs one endpoint as a long-lived process: IRC transport plus a
// harness adapter. This is the manual-use path for connecting an existing
// OpenCode session or a pi session to peers over IRC.
func (c *CLI) agent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(c.errOut)

	name := fs.String("name", "", "endpoint name (also the IRC nick)")
	harness := fs.String("harness", "", "harness: opencode | pi | claude")

	ircHost := fs.String("irc-host", "127.0.0.1", "IRC server host")
	ircPort := fs.Int("irc-port", 6667, "IRC server port")
	ircTLS := fs.Bool("irc-tls", false, "use TLS for IRC")
	ircFP := fs.String("irc-fingerprint", "", "hex SHA-256 of server leaf cert (required with --irc-tls)")
	ircPass := fs.String("irc-password", "", "IRC SASL password")
	announceTo := fs.String("announce-to", "", "send a one-time DM to this peer after connect")
	announceBody := fs.String("announce", "", "body of the announce DM")
	peer := fs.String("peer", "", "peer nick and enable local jkp say control")
	controlFile := fs.String("control-file", "", "path for local jkp agent control metadata")
	controlSession := fs.String("control-session", "", "session identity recorded for jkp say routing")
	controlRole := fs.String("control-role", "", "jkp role for local control state: host | guest")
	controlLog := fs.String("control-log", "", "log file path recorded for jkp log")
	mintGuests := fs.Bool("mint-guests", false, "accept shared-invite guest joins (host only)")
	joinNick := fs.String("join-nick", "join", "shared bootstrap nick for guest joins")
	operAddr := fs.String("oper-addr", "127.0.0.1:6667", "local Ergo operator address for guest account minting")
	operUser := fs.String("oper-user", "admin", "local Ergo operator username")
	operPass := fs.String("oper-pass", "jkp-test-oper-pw", "local Ergo operator password")
	ocReplyTimeout := fs.Duration("oc-reply-timeout", 5*time.Minute, "OpenCode reply wait timeout")

	// OpenCode
	ocURL := fs.String("oc-url", "http://127.0.0.1:4096", "OpenCode server URL")
	ocUser := fs.String("oc-user", "opencode", "OpenCode basic-auth username")
	ocPass := fs.String("oc-password", "", "OpenCode basic-auth password")
	ocSession := fs.String("oc-session", "", "OpenCode session ID to bind")

	// pi
	piBin := fs.String("pi-bin", "pi", "pi binary path")
	piSession := fs.String("pi-session", "", "pi session ID")
	piDir := fs.String("pi-dir", "", "pi session directory")
	piProvider := fs.String("pi-provider", "anthropic", "pi provider")
	piModel := fs.String("pi-model", "", "pi model")
	claudeBin := fs.String("claude-bin", "claude", "claude binary path")
	claudeForkFrom := fs.String("claude-fork-from", "", "Claude Code session ID to fork on first turn")
	claudeDir := fs.String("claude-dir", "", "Claude Code project directory")
	claudeStateFile := fs.String("claude-state-file", "", "file persisting background Claude sidecar session ID")
	claudeModel := fs.String("claude-model", "", "optional Claude model override")
	claudeTurnTimeout := fs.Duration("claude-turn-timeout", 5*time.Minute, "Claude turn timeout")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if *name == "" {
		return fmt.Errorf("%w: --name is required", ErrUsage)
	}
	if *ircPass == "" {
		return fmt.Errorf("%w: --irc-password is required", ErrUsage)
	}
	if *ircTLS && strings.TrimSpace(*ircFP) == "" {
		return fmt.Errorf("%w: --irc-tls requires --irc-fingerprint", ErrUsage)
	}
	if err := validateAgentControlFlags(*peer, *controlFile, *controlSession, *controlRole, *mintGuests, *joinNick); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if *mintGuests && *harness != "opencode" {
		return fmt.Errorf("%w: --mint-guests requires --harness opencode", ErrUsage)
	}

	var h runner.Harness
	switch *harness {
	case "opencode":
		if *ocSession == "" {
			return fmt.Errorf("%w: --oc-session is required for opencode", ErrUsage)
		}
		sessionID := *ocSession
		if sessionID == "auto" {
			discovered, err := discoverOCSession(*ocURL, *ocUser, *ocPass)
			if err != nil {
				return fmt.Errorf("discover OpenCode session: %w", err)
			}
			sessionID = discovered
			fmt.Fprintf(c.out, "jkp: discovered OpenCode session %s\n", sessionID)
		}
		h = ocadapter.New(ocadapter.Config{
			OpenCodeURL:  *ocURL,
			Username:     *ocUser,
			Password:     *ocPass,
			SessionID:    sessionID,
			EndpointID:   *name,
			PollInterval: 250 * time.Millisecond,
			ReplyTimeout: *ocReplyTimeout,
		})
	case "pi":
		if *piSession == "" {
			return fmt.Errorf("%w: --pi-session is required for pi", ErrUsage)
		}
		h = piadapter.New(piadapter.Config{
			PiBinary:    *piBin,
			SessionID:   *piSession,
			SessionDir:  *piDir,
			Provider:    *piProvider,
			Model:       *piModel,
			EndpointID:  *name,
			TurnTimeout: 5 * time.Minute,
		})
	case "claude":
		stateFile := *claudeStateFile
		if stateFile == "" {
			if roomDir, err := c.RoomDir(); err == nil {
				stateFile = filepath.Join(roomDir, "claude-"+*name+".json")
			}
		}
		h = claudeadapter.New(claudeadapter.Config{ClaudeBinary: *claudeBin, ForkFrom: *claudeForkFrom, ProjectDir: *claudeDir, StateFile: stateFile, Model: *claudeModel, EndpointID: *name, TurnTimeout: *claudeTurnTimeout, Warn: c.out})
	default:
		return fmt.Errorf("%w: --harness must be opencode, pi, or claude", ErrUsage)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config := runner.Config{
		EndpointID:     *name,
		Harness:        *harness,
		IRCHost:        *ircHost,
		IRCPort:        *ircPort,
		IRCTLS:         *ircTLS,
		IRCCertFP:      *ircFP,
		IRCPassword:    *ircPass,
		Log:            c.out,
		AnnounceTo:     *announceTo,
		AnnounceBody:   *announceBody,
		Peer:           *peer,
		ControlFile:    *controlFile,
		ControlRole:    *controlRole,
		ControlSession: *controlSession,
		ControlLogFile: *controlLog,
		ControlOpenCodeURL: func() string {
			if *harness == "opencode" {
				return *ocURL
			}
			return ""
		}(),
	}
	if *mintGuests {
		registrar := ergoacct.Registrar{Addr: *operAddr, OperUser: *operUser, OperPass: *operPass}
		minter := ergoacct.Minter{Register: registrar.RegisterNew}
		config.MintGuests = true
		config.JoinNick = *joinNick
		config.MintGuest = minter.Mint
	}
	return runner.Run(ctx, config, h)
}

func validateAgentControlFlags(peer, path, sessionID, role string, mintGuests bool, joinNick string) error {
	if path == "" {
		if peer != "" || sessionID != "" || role != "" || mintGuests {
			return fmt.Errorf("--peer/--control-file/--control-session/--control-role are required together")
		}
		return nil
	}
	if sessionID == "" || (role != "host" && role != "guest") {
		return fmt.Errorf("--control-file, --control-session and --control-role must be used together")
	}
	if role == "guest" && peer == "" {
		return fmt.Errorf("guest agents require --peer")
	}
	if role == "host" && mintGuests && joinNick == "" {
		return fmt.Errorf("--mint-guests requires --join-nick")
	}
	if mintGuests && role != "host" {
		return fmt.Errorf("--mint-guests is only valid for host agents")
	}
	if !mintGuests && role == "host" && peer == "" {
		return fmt.Errorf("host agents without --peer must use --mint-guests")
	}
	return nil
}

// host runs `jkp host`: start Ergo, register accounts, bind the OpenCode
// session, spawn the agent, and print a pasteable invite. Safe to call twice.
func (c *CLI) host(args []string) error {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	lanIP := fs.String("lan-ip", "", "override detected LAN IP")
	roomDir := fs.String("room-dir", "", "override room directory")
	ocSession := fs.String("oc-session", "", "OpenCode session to bind (defaults to current/newest unbound)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}

	deps, err := host.RealDeps(*roomDir, *lanIP, *ocSession)
	if err != nil {
		return err
	}
	h := host.New(deps)
	return h.Run(c.out)
}

func (c *CLI) version() error {
	fmt.Fprintln(c.out, Version)
	return nil
}

func (c *CLI) say(args []string) error {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	to := fs.String("to", "", "guest nick to message (host only)")
	sessionFlag := fs.String("session", "", "bound session ID for this pane")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	body := strings.TrimSpace(strings.Join(fs.Args(), " "))
	dir, err := c.RoomDir()
	if err != nil {
		return fmt.Errorf("jkp room directory: %w", err)
	}
	infos, err := control.LoadAgents(dir)
	if err != nil {
		return err
	}
	info, err := chooseSayAgent(infos, resolvePaneSession(*sessionFlag, c.Env), func(info control.Info) (int64, error) {
		updated, ok := host.OpenCodeSessionUpdated(info.OpenCodeURL, c.Env("OPENCODE_SERVER_PASSWORD"), info.SessionID)
		if !ok {
			return 0, fmt.Errorf("cannot read OpenCode session %s from %s", info.SessionID, info.OpenCodeURL)
		}
		return updated, nil
	})
	if err != nil {
		return err
	}
	controlFile, err := control.Path(dir, info.Role, info.Nick)
	if err != nil {
		return err
	}
	client := control.NewClient(controlFile)
	if body == "" {
		if info.Role != "host" || *to != "" {
			return fmt.Errorf("%w: usage: jkp say -- <text>", ErrUsage)
		}
		snapshot, err := client.Roster(context.Background())
		if err != nil {
			return err
		}
		guests := strings.Join(snapshot.Guests, ", ")
		if guests == "" {
			guests = "none"
		}
		last := snapshot.Last
		if last == "" {
			last = "none"
		}
		fmt.Fprintf(c.out, "guests: %s\nlast speaker: %s\n", guests, last)
		return nil
	}
	if info.Role == "guest" && *to != "" && *to != info.Peer {
		return fmt.Errorf("guests can only message the host (%s)", info.Peer)
	}
	target, err := client.Send(context.Background(), *to, body)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "jkp: sent to %s as %s\n", target, info.Nick)
	return nil
}

func resolvePaneSession(session string, getenv func(string) string) string {
	if session != "" && !claudeadapter.IsUnexpandedPlaceholder(session) {
		return strings.TrimSpace(session)
	}
	return saySessionID(getenv)
}

func (c *CLI) logCmd(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	sessionFlag := fs.String("session", "", "bound session ID for this pane")
	lines := fs.Int("lines", 40, "number of recent log lines")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if fs.NArg() > 0 || *lines <= 0 {
		return fmt.Errorf("%w: usage: jkp log [--session <id>] [--lines <n>]", ErrUsage)
	}
	dir, err := c.RoomDir()
	if err != nil {
		return fmt.Errorf("jkp room directory: %w", err)
	}
	infos, err := control.LoadAgents(dir)
	if err != nil {
		return err
	}
	info, err := chooseSayAgent(infos, resolvePaneSession(*sessionFlag, c.Env), func(info control.Info) (int64, error) {
		updated, ok := host.OpenCodeSessionUpdated(info.OpenCodeURL, c.Env("OPENCODE_SERVER_PASSWORD"), info.SessionID)
		if !ok {
			return 0, fmt.Errorf("cannot read OpenCode session %s from %s", info.SessionID, info.OpenCodeURL)
		}
		return updated, nil
	})
	if err != nil {
		return err
	}
	logPath := info.LogFile
	if logPath == "" {
		if info.Role == "host" {
			logPath = filepath.Join(os.TempDir(), "jkp-host-agent.log")
		} else {
			logPath = filepath.Join(os.TempDir(), "jkp-guest-"+info.Nick+".log")
		}
	}
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) || (err == nil && len(bytes.TrimSpace(data)) == 0) {
		fmt.Fprintf(c.out, "jkp: no log output yet (%s)\n", logPath)
		return nil
	}
	if err != nil {
		return err
	}
	entries := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(entries) > *lines {
		entries = entries[len(entries)-*lines:]
	}
	for _, line := range entries {
		if _, err := fmt.Fprintln(c.out, line); err != nil {
			return err
		}
	}
	return nil
}

func saySessionID(getenv func(string) string) string {
	if getenv == nil {
		return ""
	}
	if s := getenv("OPENCODE_SESSION_ID"); s != "" {
		return s
	}
	return getenv("JKP_OC_SESSION")
}

func chooseSayAgent(infos []control.Info, sessionOverride string, updated func(control.Info) (int64, error)) (control.Info, error) {
	if len(infos) == 0 {
		return control.Info{}, fmt.Errorf("not in a room; run jkp host or jkp join first (jkp help)")
	}
	if sessionOverride != "" {
		for _, info := range infos {
			if info.SessionID == sessionOverride {
				return info, nil
			}
		}
		return control.Info{}, fmt.Errorf("no live jkp agent is bound to OpenCode session %s", sessionOverride)
	}
	if len(infos) == 1 {
		return infos[0], nil
	}
	var best control.Info
	var bestUpdated int64
	for i, info := range infos {
		if info.OpenCodeURL == "" {
			return control.Info{}, fmt.Errorf("multiple local jkp agents; current session cannot be identified (set JKP_OC_SESSION or pass --session <id>)")
		}
		updatedAt, err := updated(info)
		if err != nil {
			return control.Info{}, err
		}
		if i == 0 || updatedAt > bestUpdated {
			best, bestUpdated = info, updatedAt
		} else if updatedAt == bestUpdated {
			return control.Info{}, fmt.Errorf("multiple local jkp agents have equally recent sessions; set JKP_OC_SESSION or pass --session <id>")
		}
	}
	return best, nil
}

func (c *CLI) reset(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%w: usage: jkp reset", ErrUsage)
	}
	dir, err := c.RoomDir()
	if err != nil {
		return fmt.Errorf("jkp room directory: %w", err)
	}
	extra := []int{}
	if r, err := room.Load(dir); err == nil && r.AgentPID > 0 {
		extra = append(extra, r.AgentPID)
	}
	if err := control.KillAgents(dir, extra...); err != nil {
		return err
	}
	if err := removeRoomFiles(dir); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "jkp: local room state cleared; Ergo left running")
	return nil
}

func removeRoomFiles(dir string) error {
	controlFiles := append(control.AgentPaths(dir), filepath.Join(dir, control.LegacyFileName))
	filesToRemove := append([]string{filepath.Join(dir, "room.json")}, controlFiles...)
	claudeFiles, err := filepath.Glob(filepath.Join(dir, "claude-*.json"))
	if err != nil {
		return err
	}
	filesToRemove = append(filesToRemove, claudeFiles...)
	for _, path := range filesToRemove {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// join decodes an invite token and spawns a detached agent that connects to
// the host over TLS. The process returns immediately so the caller (e.g. pi
// extension) can tail logs and keep the TUI responsive.
func (c *CLI) join(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	inviteTok := fs.String("invite", "", "invite token (jkp1...)")
	harness := fs.String("harness", "", "guest harness: opencode | pi | claude")
	ocSessionOverride := fs.String("oc-session", "", "OpenCode session to bind (defaults to current/newest unbound)")
	piSession := fs.String("pi-session", "", "pi session ID")
	claudeBin := fs.String("claude-bin", "", "claude binary path override")
	claudeForkFrom := fs.String("claude-fork-from", "", "Claude Code session ID to fork")
	claudeDir := fs.String("claude-dir", "", "Claude Code project directory")
	claudeModel := fs.String("claude-model", "", "optional Claude model override")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	tok := *inviteTok
	if tok == "" && fs.NArg() > 0 {
		tok = fs.Arg(0)
	}
	if tok == "" {
		return fmt.Errorf("%w: invite token required (jkp join <token>)", ErrUsage)
	}
	if *harness != "opencode" && *harness != "pi" && *harness != "claude" {
		return fmt.Errorf("%w: --harness must be opencode, pi, or claude", ErrUsage)
	}
	if *harness == "pi" && *piSession == "" {
		return fmt.Errorf("%w: --pi-session is required", ErrUsage)
	}

	inv, err := invite.Decode(tok)
	if err != nil {
		return fmt.Errorf("invalid invite: %w", err)
	}

	roomDir, err := c.RoomDir()
	if err != nil {
		return fmt.Errorf("jkp room directory: %w", err)
	}
	cleaned, err := control.CleanLegacyFiles(roomDir)
	if err != nil {
		return err
	}
	if cleaned {
		fmt.Fprintln(c.out, "jkp: cleared leftover guest state from an older jkp")
	}
	var ocURL, ocSession string
	if *harness == "claude" && !claudeadapter.IsUnexpandedPlaceholder(*claudeForkFrom) {
		if err := control.SessionConflict(roomDir, strings.TrimSpace(*claudeForkFrom), ""); err != nil {
			return err
		}
	}
	if *harness == "opencode" {
		getenv := c.Env
		if *ocSessionOverride != "" {
			getenv = func(key string) string {
				if key == "JKP_OC_SESSION" {
					return *ocSessionOverride
				}
				return c.Env(key)
			}
		}
		var discErr error
		ocURL, ocSession, discErr = host.DiscoverOpenCodeIn(getenv, roomDir)
		if discErr != nil {
			return discErr
		}
		if err := control.SessionConflict(roomDir, ocSession, ""); err != nil {
			return err
		}
	}
	addr := net.JoinHostPort(inv.Host, fmt.Sprint(inv.Port))
	if err := irc.DialTLSPinned(addr, inv.FP); err != nil {
		return fmt.Errorf("cannot reach %s over TLS: %w\nOn the host Mac, allow incoming TCP %d, then paste the invite again.", addr, err, inv.Port)
	}
	nick, password, err := joinclient.RequestGrant(context.Background(), func(cfg irc.RealConfig) joinclient.Client {
		return irc.NewRealClient(cfg)
	}, inv)
	if err != nil {
		return err
	}
	args2 := joinAgentArgs(inv, nick, password, *harness, *piSession, ocURL, ocSession, c.Env("OPENCODE_SERVER_PASSWORD"))
	controlSession := *piSession
	if *harness == "opencode" {
		controlSession = ocSession
	}
	logFile := filepath.Join(os.TempDir(), "jkp-guest-"+nick+".log")
	warned := false
	if *harness == "claude" {
		var resolvedForkFrom string
		controlSession, resolvedForkFrom, warned = resolveClaudeJoinSession(*claudeForkFrom, nick)
		if warned {
			fmt.Fprintln(c.out, "jkp: warning: no Claude session ID to fork; starting a fresh background Claude session")
		}
		args2 = joinClaudeAgentArgs(inv, nick, password, claudeJoinConfig{Bin: strings.TrimSpace(*claudeBin), ForkFrom: resolvedForkFrom, Dir: cleanOptionalValue(*claudeDir), StateFile: filepath.Join(roomDir, "claude-"+nick+".json"), Model: strings.TrimSpace(*claudeModel)})
	}
	controlArgs, err := controlAgentArgsWithLog(roomDir, "guest", nick, inv.Peer, controlSession, logFile)
	if err != nil {
		return err
	}
	args2 = append(args2, controlArgs...)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args2...)
	lf, err := os.Create(logFile)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	go cmd.Wait()
	if *harness == "claude" {
		writeClaudeJoinResult(c.out, nick, inv.Peer, !warned, logFile)
	} else {
		writeJoinResult(c.out, nick, inv.Peer, ocSession, logFile)
	}
	return nil
}

type claudeJoinConfig struct {
	Bin, ForkFrom, Dir, StateFile, Model string
}

func cleanOptionalValue(s string) string {
	if claudeadapter.IsUnexpandedPlaceholder(s) {
		return ""
	}
	return strings.TrimSpace(s)
}

func resolveClaudeJoinSession(forkFrom, nick string) (controlSession, resolvedForkFrom string, warned bool) {
	resolvedForkFrom = cleanOptionalValue(forkFrom)
	if resolvedForkFrom == "" {
		return "claude-" + nick, "", true
	}
	return resolvedForkFrom, resolvedForkFrom, false
}

func joinClaudeAgentArgs(inv invite.Invite, nick, password string, cfg claudeJoinConfig) []string {
	args := []string{"agent", "--name", nick, "--harness", "claude", "--claude-state-file", cfg.StateFile,
		"--irc-host", inv.Host, "--irc-port", fmt.Sprint(inv.Port), "--irc-tls", "--irc-fingerprint", inv.FP, "--irc-password", password}
	for _, option := range []struct{ flag, value string }{
		{"--claude-bin", cfg.Bin}, {"--claude-fork-from", cfg.ForkFrom},
		{"--claude-dir", cfg.Dir}, {"--claude-model", cfg.Model},
	} {
		if option.value != "" {
			args = append(args, option.flag, option.value)
		}
	}
	return args
}

func writeClaudeJoinResult(out io.Writer, nick, peer string, forked bool, logFile string) {
	fmt.Fprintf(out, "jkp: joined as %s, talking to %s\n", nick, peer)
	fmt.Fprintln(out, "jkp: Join is silent; use jkp say to talk to the host, and jkp log to inspect recent messages")
	if forked {
		fmt.Fprintln(out, "jkp: privacy: background Claude forks this window's conversation; anything in that conversation can reach the host when answering")
	}
	fmt.Fprintf(out, "jkp: log at %s\n", logFile)
}

func writeJoinResult(out io.Writer, nick, peer, sessionID, logFile string) {
	fmt.Fprintf(out, "jkp: joined as %s, talking to %s\n", nick, peer)
	fmt.Fprintln(out, "jkp: Join is silent; use jkp say to talk to the host")
	if sessionID != "" {
		fmt.Fprintf(out, "jkp: bound OpenCode session %s\n", sessionID)
	}
	fmt.Fprintf(out, "jkp: log at %s\n", logFile)
}

func joinAgentArgs(inv invite.Invite, nick, password, harness, piSession, ocURL, ocSession, ocPassword string) []string {
	args := []string{"agent", "--name", nick, "--harness", harness}
	if harness == "pi" {
		args = append(args, "--pi-session", piSession)
	} else {
		args = append(args, "--oc-url", ocURL, "--oc-session", ocSession, "--oc-password", ocPassword)
	}
	return append(args,
		"--irc-host", inv.Host,
		"--irc-port", fmt.Sprint(inv.Port),
		"--irc-tls",
		"--irc-fingerprint", inv.FP,
		"--irc-password", password,
	)
}

func controlAgentArgs(roomDir, role, nick, peer, sessionID string) ([]string, error) {
	return controlAgentArgsWithLog(roomDir, role, nick, peer, sessionID, "")
}

func controlAgentArgsWithLog(roomDir, role, nick, peer, sessionID, logFile string) ([]string, error) {
	path, err := control.Path(roomDir, role, nick)
	if err != nil {
		return nil, err
	}
	args := []string{"--peer", peer, "--control-file", path, "--control-session", sessionID, "--control-role", role}
	if logFile != "" {
		args = append(args, "--control-log", logFile)
	}
	return args, nil
}

// serve validates configuration and binds the local adapter API. It fails
// closed when no local token is available; serving unauthenticated would let
// any local process drive the endpoints.
func (c *CLI) serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	token := fs.String("token", "", "local API bearer token (or JKP_LOCAL_TOKEN)")
	addr := fs.String("addr", localapi.DefaultListenAddr, "listen address")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}

	resolved := strings.TrimSpace(*token)
	if resolved == "" {
		resolved = strings.TrimSpace(c.Env("JKP_LOCAL_TOKEN"))
	}

	ln, err := c.Listen(*addr, resolved)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "jkp: listening on %s\n", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.Serve(ctx, ln, resolved)
}

// Listen validates the bind address and token, then binds a TCP listener. It
// fails closed on a missing token or a non-loopback address so a misconfigured
// core cannot expose the adapter API to the network.
func (c *CLI) Listen(addr, token string) (net.Listener, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: a local token is required; pass --token or set JKP_LOCAL_TOKEN", ErrUsage)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid listen address %q", ErrUsage, addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("%w: refusing to bind non-loopback address %q", ErrUsage, addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return ln, nil
}

// Serve exposes the local adapter API on ln until ctx is cancelled. It returns
// http.ErrServerClosed on clean shutdown.
func (c *CLI) Serve(ctx context.Context, ln net.Listener, token string) error {
	srv := localapi.NewServer(c.svc, token)
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		err := <-errc
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (c *CLI) endpoint(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected an endpoint subcommand", ErrUsage)
	}
	switch args[0] {
	case "register":
		return c.endpointRegister(args[1:])
	case "enable":
		return c.endpointToggle(args[1:], (*core.Service).Enable)
	case "disable":
		return c.endpointToggle(args[1:], (*core.Service).Disable)
	case "list":
		return c.endpointList()
	default:
		return fmt.Errorf("%w: unknown endpoint subcommand %q", ErrUsage, args[0])
	}
}

func (c *CLI) endpointRegister(args []string) error {
	fs := flag.NewFlagSet("endpoint register", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	id := fs.String("id", "", "endpoint id")
	harness := fs.String("harness", "", "harness (opencode|pi|claude-code)")
	sessionID := fs.String("session-id", "", "exact harness session id")
	nick := fs.String("nick", "", "IRC nick for this endpoint")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if *id == "" || *harness == "" || *sessionID == "" || *nick == "" {
		return fmt.Errorf("%w: --id, --harness, --session-id and --nick are required", ErrUsage)
	}
	err := c.svc.Register(core.Endpoint{
		ID: protocol.EndpointID(*id), Harness: *harness,
		SessionID: *sessionID, Nick: *nick,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "registered %s (%s) session=%s nick=%s\n", *id, *harness, *sessionID, *nick)
	return nil
}

func (c *CLI) endpointToggle(args []string, fn func(*core.Service, protocol.EndpointID) error) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: expected exactly one endpoint id", ErrUsage)
	}
	if err := fn(c.svc, protocol.EndpointID(args[0])); err != nil {
		return err
	}
	return nil
}

func (c *CLI) endpointList() error {
	eps := c.svc.List()
	for _, ep := range eps {
		fmt.Fprintf(c.out, "%s\tharness=%s\tsession=%s\tnick=%s\n",
			ep.ID, ep.Harness, ep.SessionID, ep.Nick)
	}
	return nil
}

// discoverOCSession finds the most recently active OpenCode session on a
// running server. This supports `--oc-session auto` for the slash-command UX
// where the caller does not know the session ID.
func discoverOCSession(baseURL, user, pass string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/session")
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("directory", cwd)
	u.RawQuery = query.Encode()
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, pass)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("OpenCode /session: %s", resp.Status)
	}
	var sessions []struct {
		ID        string `json:"id"`
		ParentID  string `json:"parentID"`
		Directory string `json:"directory"`
		Time      struct {
			Updated int64 `json:"updated"`
		} `json:"time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return "", err
	}
	var best *struct {
		ID        string `json:"id"`
		ParentID  string `json:"parentID"`
		Directory string `json:"directory"`
		Time      struct {
			Updated int64 `json:"updated"`
		} `json:"time"`
	}
	for i := range sessions {
		s := &sessions[i]
		if s.ParentID != "" || s.ID == "" {
			continue
		}
		if s.Directory != "" {
			dir := s.Directory
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			} else if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
			if filepath.Clean(dir) != filepath.Clean(cwd) {
				continue
			}
		}
		if best == nil || s.Time.Updated > best.Time.Updated {
			best = s
		}
	}
	if best == nil {
		return "", fmt.Errorf("no top-level OpenCode sessions found for %s", cwd)
	}
	return best.ID, nil
}
