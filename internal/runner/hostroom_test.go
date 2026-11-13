package runner

import (
	"crypto/ecdh"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinproto"
	"github.com/joey/jkp/internal/protocol"
)

type testMinter struct {
	mu    sync.Mutex
	next  int
	err   error
	known []string
}

func (m *testMinter) Mint() (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return "", "", m.err
	}
	m.next++
	nick := fmt.Sprintf("g%d", m.next)
	pass := "pw-" + nick
	m.known = append(m.known, nick)
	return nick, pass, nil
}

type testHostSender struct {
	mu       sync.Mutex
	messages []protocol.OutboundMessage
}

func (s *testHostSender) Send(msg protocol.OutboundMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *testHostSender) snapshot() []protocol.OutboundMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocol.OutboundMessage(nil), s.messages...)
}

func TestJoinRequestMintsAndSealsGrant(t *testing.T) {
	request, private, err := joinproto.NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	minter := &testMinter{}
	sender := &testHostSender{}
	var monitored string
	hr := newHostRoom("host", "join", minter, sender, func(nick string) error { monitored = nick; return nil }, nil)
	if handled := hr.HandleInbound(dmEvent("join", joinproto.FormatJoin(request))); handled {
		t.Fatal("join protocol event was forwarded to the harness")
	}
	messages := sender.snapshot()
	if len(messages) != 1 || messages[0].Target != "join" {
		t.Fatalf("grant sends = %#v", messages)
	}
	grant, err := joinproto.OpenGrant(messages[0].Body, request.Nonce, private)
	if err != nil || grant.Nick != "g1" || grant.Password != "pw-g1" {
		t.Fatalf("opened grant = %#v, %v", grant, err)
	}
	if monitored != "g1" || !sameStrings(hr.Roster().Guests, []string{"g1"}) {
		t.Fatalf("monitor=%q roster=%#v", monitored, hr.Roster())
	}
}

func TestJoinRequestFromNonJoinNickIsDropped(t *testing.T) {
	request, _, err := joinproto.NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	minter, sender := &testMinter{}, &testHostSender{}
	hr := newHostRoom("host", "join", minter, sender, func(string) error { return nil }, nil)
	if handled := hr.HandleInbound(dmEvent("g9", joinproto.FormatJoin(request))); handled {
		t.Fatal("join protocol from non-bootstrap nick was forwarded")
	}
	if minter.next != 0 || len(sender.snapshot()) != 0 {
		t.Fatalf("mint calls=%d sends=%#v", minter.next, sender.snapshot())
	}
}

func TestPlainTextFromJoinNickIsDropped(t *testing.T) {
	sender := &testHostSender{}
	hr := newHostRoom("host", "join", &testMinter{}, sender, func(string) error { return nil }, nil)
	if handled := hr.HandleInbound(dmEvent("join", "please help")); handled {
		t.Fatal("plain text from bootstrap account was forwarded")
	}
}

func TestProtocolLineFromGuestIsDropped(t *testing.T) {
	sender := &testHostSender{}
	hr := newHostRoom("host", "join", &testMinter{}, sender, func(string) error { return nil }, nil)
	if handled := hr.HandleInbound(dmEvent("g1", "JKP-GRANT nonce key cipher")); handled {
		t.Fatal("protocol line from guest was forwarded")
	}
}

func TestMintFailureSendsDenyAndDoesNotForward(t *testing.T) {
	request, _, err := joinproto.NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	minter := &testMinter{err: errors.New("operator unavailable")}
	sender := &testHostSender{}
	var logged string
	hr := newHostRoom("host", "join", minter, sender, func(string) error { return nil }, func(format string, args ...any) { logged += fmt.Sprintf(format, args...) })
	if handled := hr.HandleInbound(dmEvent("join", joinproto.FormatJoin(request))); handled {
		t.Fatal("failed join request was forwarded")
	}
	messages := sender.snapshot()
	if len(messages) != 1 {
		t.Fatalf("deny sends = %#v", messages)
	}
	deny, err := joinproto.ParseDeny(messages[0].Body)
	if err != nil || deny.Nonce != request.Nonce || deny.Reason != "operator unavailable" {
		t.Fatalf("deny = %#v, %v", deny, err)
	}
	if logged == "" {
		t.Fatal("mint failure was not logged")
	}
}

func TestTwoConcurrentJoinRequestsReceivePrivateGrants(t *testing.T) {
	requests := make([]joinproto.Request, 2)
	keys := make([]*ecdh.PrivateKey, 2)
	for i := range requests {
		req, private, err := joinproto.NewRequest()
		if err != nil {
			t.Fatal(err)
		}
		requests[i] = req
		keys[i] = private
	}
	minter, sender := &testMinter{}, &testHostSender{}
	hr := newHostRoom("host", "join", minter, sender, func(string) error { return nil }, nil)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hr.HandleInbound(dmEvent("join", joinproto.FormatJoin(requests[i])))
		}(i)
	}
	wg.Wait()
	messages := sender.snapshot()
	if len(messages) != 2 {
		t.Fatalf("grant messages = %d, want 2", len(messages))
	}
	seen := map[string]bool{}
	for _, msg := range messages {
		opened := false
		for i, req := range requests {
			grant, err := joinproto.OpenGrant(msg.Body, req.Nonce, keys[i])
			if err == nil {
				seen[grant.Nick] = true
				opened = true
				break
			}
		}
		if !opened {
			t.Fatalf("no request could open grant %q", msg.Body)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("granted nicknames = %v", seen)
	}
}

func TestGuestDMUpdatesLastSpeaker(t *testing.T) {
	hr := newHostRoom("host", "join", &testMinter{}, &testHostSender{}, func(string) error { return nil }, nil)
	if !hr.HandleInbound(dmEvent("g1", "hello host")) || hr.Roster().Last != "g1" {
		t.Fatalf("guest message not forwarded or last speaker not updated: %#v", hr.Roster())
	}
}

func TestPresenceOfflineRemovesGuest(t *testing.T) {
	hr := newHostRoom("host", "join", &testMinter{}, &testHostSender{}, func(string) error { return nil }, nil)
	hr.roster.Spoke("g1")
	hr.HandlePresence(irc.PresenceEvent{Nick: "g1", Online: false})
	if snap := hr.Roster(); snap.Last != "" || len(snap.Guests) != 0 {
		t.Fatalf("roster after offline = %#v", snap)
	}
}

func TestHostSayResolvesThroughRoster(t *testing.T) {
	sender := &testHostSender{}
	hr := newHostRoom("host", "join", &testMinter{}, sender, func(string) error { return nil }, nil)
	hr.roster.Spoke("g1")
	hr.roster.Add("g2")
	target, err := hr.Send("", "default")
	if err != nil || target != "g1" {
		t.Fatalf("default Send target=%q err=%v", target, err)
	}
	target, err = hr.Send("g2", "direct")
	if err != nil || target != "g2" || hr.Roster().Last != "g2" {
		t.Fatalf("direct Send target=%q err=%v snapshot=%#v", target, err, hr.Roster())
	}
	before := len(sender.snapshot())
	hr.roster.Remove("g1")
	hr.roster.Remove("g2")
	if _, err := hr.Send("", "no one"); err == nil {
		t.Fatal("Send without a last speaker succeeded")
	}
	if got := len(sender.snapshot()); got != before {
		t.Fatalf("failed route sent a message: before=%d after=%d", before, got)
	}
}

func dmEvent(nick, body string) protocol.InboundEvent {
	return protocol.InboundEvent{ID: "evt-" + nick, EndpointID: "host", Kind: protocol.KindDM, Target: "host", Sender: protocol.Identity{Nick: nick}, Body: body}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
