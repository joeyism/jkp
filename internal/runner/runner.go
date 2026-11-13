// Package runner wires an IRC transport, the collaboration core, and one
// harness adapter into a single process. This is the manual-use path: one
// `jkp agent` process per endpoint, each holding its own IRC identity and
// harness binding, talking to peers over IRC.
//
// It is deliberately not the local-API multi-adapter topology. Each runner is
// self-contained: it receives IRC events, drives the harness, and sends replies
// and peer-directed messages straight back over IRC.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/jkpclient"
	"github.com/joey/jkp/internal/joinproto"
	"github.com/joey/jkp/internal/protocol"
)

// Harness turns one inbound message into a reply. Adapters implement this.
type Harness interface {
	Handle(ctx context.Context, ev jkpclient.Event) (string, error)
}

// ErrorNotifier lets a stateful harness show a one-way delivery notice to its
// local agent without waiting for or forwarding the resulting assistant turn.
type ErrorNotifier interface {
	NotifyError(ctx context.Context, notice string) error
}

func reportIRCServerError(ctx context.Context, h Harness, command, detail string, logf func(string, ...any)) {
	logf("jkp: IRC server error (%s): %s", command, detail)
	if notifier, ok := h.(ErrorNotifier); ok {
		notice := fmt.Sprintf(
			"[JKP-DELIVERY-ERROR] The IRC server rejected your most recent send (%s: %s). It may not have been delivered. Do NOT retry in a loop; review the previous message and tell your user if needed.",
			command, detail,
		)
		if err := notifier.NotifyError(ctx, notice); err != nil {
			logf("jkp: notify session of IRC server rejection failed: %v", err)
		}
	}
}

// Config points the runner at an IRC identity and a harness adapter.
type Config struct {
	// EndpointID is the jkp endpoint name. It doubles as the IRC nick.
	EndpointID string
	// Harness selects the adapter (for display only).
	Harness string

	// IRC transport.
	IRCHost     string
	IRCPort     int
	IRCTLS      bool
	IRCCertFP   string
	IRCPassword string
	Channels    []string

	// LogLevel controls progress output.
	Log io.Writer

	// AnnounceTo and AnnounceBody send a one-time DM after connect.
	AnnounceTo   string
	AnnounceBody string

	// ControlFile enables the authenticated loopback `jkp say` endpoint.
	ControlFile        string
	ControlRole        string
	Peer               string
	ControlSession     string
	ControlOpenCodeURL string
	ControlLogFile     string
	// MintGuests enables shared-invite guest minting on the host.
	MintGuests bool
	JoinNick   string
	MintGuest  func() (nick, password string, err error)
}

// Run connects to IRC, binds the harness adapter, and processes events until
// ctx is cancelled. One event is handled at a time so replies cannot
// cross-route.
func Run(ctx context.Context, cfg Config, h Harness) error {
	if cfg.EndpointID == "" {
		return fmt.Errorf("endpoint id is required")
	}
	if h == nil {
		return fmt.Errorf("harness adapter is required")
	}
	logf := func(format string, args ...any) {
		if cfg.Log != nil {
			fmt.Fprintf(cfg.Log, format+"\n", args...)
		}
	}

	client := irc.NewRealClient(irc.RealConfig{
		Host:     cfg.IRCHost,
		Port:     cfg.IRCPort,
		TLS:      cfg.IRCTLS,
		CertFP:   cfg.IRCCertFP,
		Nick:     cfg.EndpointID,
		Account:  cfg.EndpointID,
		Password: cfg.IRCPassword,
		Channels: cfg.Channels,
		OnServerError: func(command, detail string) {
			go func() {
				serverErrorCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				reportIRCServerError(serverErrorCtx, h, command, detail, logf)
			}()
		},
	})

	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("irc connect: %w", err)
	}
	defer func() { _ = client.Disconnect() }()
	logf("jkp: connected to IRC as %s (harness=%s)", cfg.EndpointID, cfg.Harness)

	// Send a one-time DM so the guest speaks first.
	if err := Announce(client, cfg.EndpointID, cfg.AnnounceTo, cfg.AnnounceBody); err != nil {
		logf("jkp: announce failed: %v", err)
	} else if cfg.AnnounceTo != "" && cfg.AnnounceBody != "" {
		logf("jkp: -> %s: %s", cfg.AnnounceTo, truncate(cfg.AnnounceBody, 80))
	}

	svc := core.NewServiceWithBounds(client, realClock{}, core.BoundsConfig{
		MaxTurns:  core.MaxAutonomousTurns,
		MaxAge:    0,
		IdleReset: 5 * time.Minute,
	})
	if err := svc.Register(core.Endpoint{
		ID:        coreEndpointID(cfg),
		Harness:   cfg.Harness,
		SessionID: cfg.EndpointID,
		Nick:      cfg.EndpointID,
	}); err != nil {
		return err
	}
	if err := svc.Enable(coreEndpointID(cfg)); err != nil {
		return err
	}

	br := irc.NewBridge(svc)
	stop := br.AttachWithErrors(coreEndpointID(cfg), client, func(err error) {
		logf("jkp: incoming IRC event rejected: %v", err)
	})
	defer stop()
	var room *hostRoom
	if cfg.MintGuests {
		if cfg.ControlRole != "host" {
			return fmt.Errorf("guest minting is only available to host agents")
		}
		if cfg.JoinNick == "" || cfg.MintGuest == nil {
			return fmt.Errorf("host guest minting requires a join nick and account minter")
		}
		room = newHostRoom(cfg.EndpointID, cfg.JoinNick, guestMinterFunc(cfg.MintGuest), client, client.Monitor, logf)
		go func() {
			for presence := range client.Presence() {
				room.HandlePresence(presence)
			}
		}()
	}
	controlHandlers := control.Handlers{Role: cfg.ControlRole, Peer: cfg.Peer}
	if room != nil {
		controlHandlers.Send = func(to, body string) (string, error) {
			target, err := room.SendHuman(to, body)
			if err == nil {
				svc.ResetConversation(protocol.Conversation{Kind: protocol.KindDM, Target: target})
			}
			return target, err
		}
		controlHandlers.Roster = room.Roster
	} else {
		controlHandlers.Send = func(to, body string) (string, error) {
			target := to
			if target == "" {
				target = cfg.Peer
			}
			if target == "" {
				return "", fmt.Errorf("no peer target configured")
			}
			id := fmt.Sprintf("say-%d", time.Now().UnixNano())
			err := client.Send(protocol.OutboundMessage{ID: id, EndpointID: coreEndpointID(cfg), Kind: protocol.KindDM, Target: target, Body: body, InReplyTo: id, HumanInitiated: true})
			if err != nil {
				logf("jkp: say to %s failed: %v", target, err)
			} else {
				svc.ResetConversation(protocol.Conversation{Kind: protocol.KindDM, Target: target})
				logf("jkp: say -> %s: %s", target, truncate(body, 80))
			}
			return target, err
		}
	}
	stopControl, err := startControl(ctx, cfg.ControlFile, cfg.ControlRole, cfg.EndpointID, cfg.Peer, cfg.ControlSession, cfg.ControlOpenCodeURL, cfg.ControlLogFile, controlHandlers)
	if err != nil {
		return err
	}
	defer stopControl()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ev, ok := svc.Next(coreEndpointID(cfg))
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		jkpEv := toJKPEvent(ev)
		if room != nil {
			if !room.HandleInbound(ev) {
				continue
			}
		} else if joinproto.IsProtocol(ev.Body) {
			logf("jkp: ignored join protocol message from %s", ev.Sender.Nick)
			continue
		}
		logf("jkp: <- %s: %s", jkpEv.SenderName(), truncate(jkpEv.Body, 80))
		reply, err := h.Handle(ctx, jkpEv)
		if err != nil {
			logf("jkp: harness error (not forwarded): %v", err)
			continue
		}

		handleReply(ctx, cfg, client, h, ev, reply, logf)
	}
}

func handleReply(ctx context.Context, cfg Config, client messageSender, h Harness, ev protocol.InboundEvent, reply string, logf func(string, ...any)) {
	parsed := jkpclient.ParseReply(reply)
	if parsed.LocalOnly {
		byteCount := len(parsed.Reply)
		logf("jkp: kept local by marker (%d bytes), not sent to %s", byteCount, ev.ReplyTarget())
		if byteCount > 240 {
			if notifier, ok := h.(ErrorNotifier); ok {
				if err := notifier.NotifyError(ctx, jkpclient.LocalOnlyNotice(ev.ReplyTarget(), byteCount)); err != nil {
					logf("jkp: notify session of local-only turn failed: %v", err)
				}
			}
		}
		return
	}

	// Send peer-directed messages over IRC to the named endpoint.
	for _, d := range parsed.Sends {
		target := d.To
		msg := protocol.OutboundMessage{
			ID: "run-" + ev.ID + "-" + target, EndpointID: coreEndpointID(cfg),
			Kind: protocol.KindDM, Target: target, Body: d.Body, InReplyTo: ev.ID,
		}
		if err := client.Send(msg); err != nil {
			logf("jkp: send to %s failed: %v", target, err)
		} else {
			logf("jkp: -> %s: %s", target, truncate(d.Body, 80))
		}
	}

	text := parsed.Reply
	if text == "" && len(parsed.Sends) > 0 {
		var peers []string
		for _, d := range parsed.Sends {
			peers = append(peers, d.To)
		}
		text = "jkp: forwarded your message to " + strings.Join(peers, ", ")
	}
	decision := jkpclient.Classify(text)
	switch decision.Action {
	case jkpclient.ActionLocalOnly:
		// ParseReply already handles a leading local-only marker before sends.
		return
	case jkpclient.ActionAckOnly:
		logf("jkp: skipped acknowledgement-only reply to %s", ev.ReplyTarget())
		return
	case jkpclient.ActionEmpty:
		return
	case jkpclient.ActionSend:
		deliverReply(ctx, cfg, client, h, ev, decision.Body, logf)
	}
}

func deliverReply(ctx context.Context, cfg Config, client messageSender, h Harness, ev protocol.InboundEvent, text string, logf func(string, ...any)) {
	out := protocol.OutboundMessage{
		ID: "run-reply-" + ev.ID, EndpointID: coreEndpointID(cfg),
		Kind: ev.Kind, Target: ev.ReplyTarget(), Body: text, InReplyTo: ev.ID,
	}
	if err := client.Send(out); err == nil {
		logf("jkp: -> %s: %s", out.Target, truncate(text, 80))
		return
	} else {
		logf("jkp: reply failed: %v", err)
		if errors.Is(err, protocol.ErrBodyTooLarge) {
			if notifier, ok := h.(ErrorNotifier); ok {
				sayCommand := "jkp say -- <part>"
				if cfg.ControlRole == "host" {
					sayCommand = fmt.Sprintf("jkp say --to %s -- <part>", out.Target)
				}
				notice := fmt.Sprintf(
					"[JKP-DELIVERY-ERROR] Your reply to %s was NOT sent: it is %d bytes (max %d). Split it into ordered parts of at most %d bytes and send them with `%s`. Keep each part within that byte limit; do not retransmit the entire answer. Start any final local status note with %s.",
					out.Target, len(text), protocol.MaxBodyBytes, protocol.SafeSplitBytes, sayCommand, jkpclient.LocalOnlyMarker,
				)
				if notifyErr := notifier.NotifyError(ctx, notice); notifyErr != nil {
					logf("jkp: notify session of oversize reply failed: %v", notifyErr)
				}
				return
			}

			// pi and Claude run without tools. Give those adapters one bounded
			// chance to condense, then never retry again if it still cannot send.
			condenseEvent := toJKPEvent(ev)
			condenseEvent.Body = fmt.Sprintf(
				"[JKP-DELIVERY-ERROR] Your previous reply to %s was NOT sent because it was %d bytes (max %d). Tools are disabled here, so rewrite a concise replacement under %d bytes now. Start any final local note with %s.",
				out.Target, len(text), protocol.MaxBodyBytes, protocol.SafeSplitBytes, jkpclient.LocalOnlyMarker,
			)
			condensed, retryErr := h.Handle(ctx, condenseEvent)
			if retryErr != nil {
				logf("jkp: condense retry failed: %v", retryErr)
				return
			}
			parsed := jkpclient.ParseReply(condensed)
			if parsed.Reply == "" || !jkpclient.Forwardable(parsed.Reply) {
				return
			}
			out.ID = "run-retry-" + ev.ID
			out.Body = parsed.Reply
			if retrySendErr := client.Send(out); retrySendErr != nil {
				logf("jkp: condensed reply failed: %v", retrySendErr)
			} else {
				logf("jkp: -> %s: %s", out.Target, truncate(out.Body, 80))
			}
			return
		}

		if notifier, ok := h.(ErrorNotifier); ok {
			notice := fmt.Sprintf(
				"[JKP-DELIVERY-ERROR] Your reply to %s could not be delivered over IRC (%v). Do NOT retry in a loop; inform your user and suggest checking `jkp log`.",
				out.Target, err,
			)
			if notifyErr := notifier.NotifyError(ctx, notice); notifyErr != nil {
				logf("jkp: notify session of delivery failure failed: %v", notifyErr)
			}
		}
	}
}

type messageSender interface {
	Send(protocol.OutboundMessage) error
}

func startControl(ctx context.Context, path, role, nick, peer, sessionID, openCodeURL, logFile string, handlers control.Handlers) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for local jkp control: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("generate local jkp control token: %w", err)
	}
	token := hex.EncodeToString(secret)
	info := control.Info{
		Addr: "http://" + listener.Addr().String(), Token: token,
		Role: role, Nick: nick, Peer: peer, PID: os.Getpid(), SessionID: sessionID, OpenCodeURL: openCodeURL,
		LogFile: logFile,
	}
	if err := control.Save(path, info); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("write local jkp control file: %w", err)
	}
	if handlers.Role == "" {
		handlers.Role = role
	}
	if handlers.Peer == "" {
		handlers.Peer = peer
	}
	server := &http.Server{Handler: control.NewHandler(token, handlers)}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if current, err := control.Load(path); err == nil && current.Token == token {
			_ = os.Remove(filepath.Clean(path))
		}
	}, nil
}

type guestMinterFunc func() (nick, password string, err error)

func (f guestMinterFunc) Mint() (string, string, error) { return f() }

func coreEndpointID(cfg Config) protocol.EndpointID {
	return protocol.EndpointID(cfg.EndpointID)
}

// Sender is the minimal interface Announce needs to send a message.
type Sender interface {
	Send(protocol.OutboundMessage) error
}

// Announce sends a one-time DM to peer after connect. It does nothing when
// peer or body is empty. This is how the guest speaks first without a human
// IRC client or a model directive.
func Announce(client Sender, from, peer, body string) error {
	if peer == "" || strings.TrimSpace(body) == "" {
		return nil
	}
	id := "announce-" + peer
	return client.Send(protocol.OutboundMessage{
		ID:         id,
		EndpointID: protocol.EndpointID(from),
		Kind:       protocol.KindDM,
		Target:     peer,
		Body:       body,
		InReplyTo:  id, // not a reply; validator still requires the field
	})
}

func toJKPEvent(ev protocol.InboundEvent) jkpclient.Event {
	return jkpclient.Event{
		ID:   ev.ID,
		Kind: string(ev.Kind),
		Body: ev.Body,
		Sender: struct {
			Nick    string `json:"nick"`
			Account string `json:"account"`
		}{Nick: ev.Sender.Nick, Account: ev.Sender.Account},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
