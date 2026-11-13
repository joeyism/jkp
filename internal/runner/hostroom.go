package runner

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinproto"
	"github.com/joey/jkp/internal/protocol"
	"github.com/joey/jkp/internal/roster"
)

type guestMinter interface {
	Mint() (nick, password string, err error)
}

type hostRoom struct {
	hostNick string
	joinNick string
	minter   guestMinter
	sender   messageSender
	monitor  func(string) error
	logf     func(string, ...any)
	roster   *roster.Roster
	joinMu   sync.Mutex
}

func newHostRoom(hostNick, joinNick string, minter guestMinter, sender messageSender, monitor func(string) error, logf func(string, ...any)) *hostRoom {
	if monitor == nil {
		monitor = func(string) error { return nil }
	}
	return &hostRoom{
		hostNick: hostNick,
		joinNick: joinNick,
		minter:   minter,
		sender:   sender,
		monitor:  monitor,
		logf:     logf,
		roster:   roster.New(),
	}
}

// HandleInbound consumes join protocol traffic and bootstrap-account messages.
// Ordinary peer DMs are recorded and returned for harness delivery.
func (h *hostRoom) HandleInbound(ev protocol.InboundEvent) bool {
	if ev.Kind == protocol.KindDM && joinproto.IsProtocol(ev.Body) {
		if strings.HasPrefix(ev.Body, "JKP-JOIN ") && ev.Sender.Nick == h.joinNick {
			h.handleJoin(ev)
		}
		return false
	}
	if ev.Sender.Nick == h.joinNick {
		return false
	}
	if ev.Kind == protocol.KindDM {
		h.roster.Spoke(ev.Sender.Nick)
	}
	return true
}

func (h *hostRoom) handleJoin(ev protocol.InboundEvent) {
	h.joinMu.Lock()
	defer h.joinMu.Unlock()
	request, err := joinproto.ParseJoin(ev.Body)
	if err != nil {
		h.log("rejected malformed guest join from %s: %v", ev.Sender.Nick, err)
		return
	}
	h.log("guest join request received from %s nonce=%s", ev.Sender.Nick, request.Nonce)
	if h.minter == nil {
		h.deny(ev, request.Nonce, "guest account minting is unavailable")
		return
	}
	nick, password, err := h.minter.Mint()
	if err != nil {
		h.log("guest account mint failed: %v", err)
		h.deny(ev, request.Nonce, err.Error())
		return
	}
	sealed, err := joinproto.SealGrant(request, joinproto.Grant{Nick: nick, Password: password})
	if err != nil {
		h.log("seal guest grant for %s failed: %v", nick, err)
		h.deny(ev, request.Nonce, "could not seal guest credentials")
		return
	}
	h.roster.Add(nick)
	if err := h.monitor(nick); err != nil {
		h.log("MONITOR %s failed: %v", nick, err)
	}
	if err := h.sendDM(ev.ID, ev.Sender.Nick, sealed); err != nil {
		h.log("send sealed grant to %s failed: %v", ev.Sender.Nick, err)
		return
	}
	h.log("guest %s joined silently", nick)
}

func (h *hostRoom) deny(ev protocol.InboundEvent, nonce, reason string) {
	body := joinproto.FormatDeny(nonce, reason)
	if err := h.sendDM(ev.ID, ev.Sender.Nick, body); err != nil {
		h.log("send join denial to %s failed: %v", ev.Sender.Nick, err)
	}
}

func (h *hostRoom) sendDM(inReplyTo, target, body string) error {
	return h.sender.Send(protocol.OutboundMessage{
		ID:         "host-room-" + inReplyTo,
		EndpointID: protocol.EndpointID(h.hostNick),
		Kind:       protocol.KindDM,
		Target:     target,
		Body:       body,
		InReplyTo:  inReplyTo,
	})
}

func (h *hostRoom) HandlePresence(ev irc.PresenceEvent) {
	if ev.Nick == "" || ev.Nick == h.joinNick {
		return
	}
	if ev.Online {
		h.roster.Add(ev.Nick)
	} else {
		h.roster.Remove(ev.Nick)
	}
}

func (h *hostRoom) Send(to, body string) (string, error) {
	return h.send(to, body, false)
}

// SendHuman sends an explicit local jkp say and marks it as the start of a
// fresh dialogue volley at the receiving endpoint.
func (h *hostRoom) SendHuman(to, body string) (string, error) {
	return h.send(to, body, true)
}

func (h *hostRoom) send(to, body string, humanInitiated bool) (string, error) {
	target, err := h.roster.Resolve(to)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("host-say-%s-%d", protocol.EndpointID(target), time.Now().UnixNano())
	if err := h.sender.Send(protocol.OutboundMessage{
		ID:             id,
		EndpointID:     protocol.EndpointID(h.hostNick),
		Kind:           protocol.KindDM,
		Target:         target,
		Body:           body,
		InReplyTo:      id,
		HumanInitiated: humanInitiated,
	}); err != nil {
		return "", err
	}
	return target, nil
}

func (h *hostRoom) Roster() roster.Snapshot { return h.roster.Snapshot() }

func (h *hostRoom) log(format string, args ...any) {
	if h.logf != nil {
		h.logf(format, args...)
	}
}
