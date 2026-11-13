package joinclient

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinproto"
	"github.com/joey/jkp/internal/protocol"
)

type fakeIRC struct {
	config     irc.RealConfig
	events     chan protocol.InboundEvent
	onSend     func(protocol.OutboundMessage)
	sent       []protocol.OutboundMessage
	connected  bool
	disconnect bool
}

func (f *fakeIRC) Connect(context.Context) error { f.connected = true; return nil }
func (f *fakeIRC) Send(msg protocol.OutboundMessage) error {
	f.sent = append(f.sent, msg)
	if f.onSend != nil {
		f.onSend(msg)
	}
	return nil
}
func (f *fakeIRC) Events() <-chan protocol.InboundEvent { return f.events }
func (f *fakeIRC) Disconnect() error                    { f.disconnect = true; return nil }

func TestRequestGrantSuccess(t *testing.T) {
	var client *fakeIRC
	newClient := func(config irc.RealConfig) Client {
		client = &fakeIRC{config: config, events: make(chan protocol.InboundEvent, 4)}
		client.onSend = func(msg protocol.OutboundMessage) {
			req, err := joinproto.ParseJoin(msg.Body)
			if err != nil {
				t.Errorf("ParseJoin: %v", err)
				return
			}
			grant, err := joinproto.SealGrant(req, joinproto.Grant{Nick: "g1a2b3c4", Password: "guest-password"})
			if err != nil {
				t.Errorf("SealGrant: %v", err)
				return
			}
			client.events <- protocol.InboundEvent{ID: "grant", Kind: protocol.KindDM, Sender: protocol.Identity{Nick: "host"}, Body: grant}
		}
		return client
	}
	gotNick, gotPass, err := requestGrant(context.Background(), newClient, testInvite(), time.Second)
	if err != nil || gotNick != "g1a2b3c4" || gotPass != "guest-password" {
		t.Fatalf("RequestGrant = %q, %q, %v", gotNick, gotPass, err)
	}
	if !client.connected || !client.disconnect || len(client.sent) != 1 || client.sent[0].Target != "host" {
		t.Fatalf("client state = %#v", client)
	}
	if !client.config.TLS || client.config.Nick != "join" || client.config.Account != "join" || client.config.CertFP != "fp" {
		t.Fatalf("bootstrap config = %#v", client.config)
	}
}

func TestRequestGrantIgnoresOtherNonces(t *testing.T) {
	newClient := func(config irc.RealConfig) Client {
		client := &fakeIRC{config: config, events: make(chan protocol.InboundEvent, 4)}
		client.onSend = func(msg protocol.OutboundMessage) {
			req, _ := joinproto.ParseJoin(msg.Body)
			other, _, _ := joinproto.NewRequest()
			wrong, _ := joinproto.SealGrant(other, joinproto.Grant{Nick: "gwrong", Password: "wrong"})
			right, _ := joinproto.SealGrant(req, joinproto.Grant{Nick: "gright", Password: "right"})
			client.events <- protocol.InboundEvent{ID: "other", Kind: protocol.KindDM, Sender: protocol.Identity{Nick: "host"}, Body: wrong}
			client.events <- protocol.InboundEvent{ID: "right", Kind: protocol.KindDM, Sender: protocol.Identity{Nick: "host"}, Body: right}
		}
		return client
	}
	nick, pass, err := requestGrant(context.Background(), newClient, testInvite(), time.Second)
	if err != nil || nick != "gright" || pass != "right" {
		t.Fatalf("RequestGrant = %q, %q, %v", nick, pass, err)
	}
}

func TestRequestGrantIgnoresNonHostSender(t *testing.T) {
	newClient := func(config irc.RealConfig) Client {
		client := &fakeIRC{config: config, events: make(chan protocol.InboundEvent, 4)}
		client.onSend = func(msg protocol.OutboundMessage) {
			req, _ := joinproto.ParseJoin(msg.Body)
			grant, _ := joinproto.SealGrant(req, joinproto.Grant{Nick: "ghost", Password: "bad"})
			client.events <- protocol.InboundEvent{ID: "fake", Kind: protocol.KindDM, Sender: protocol.Identity{Nick: "g9"}, Body: grant}
			grant, _ = joinproto.SealGrant(req, joinproto.Grant{Nick: "g1", Password: "good"})
			client.events <- protocol.InboundEvent{ID: "real", Kind: protocol.KindDM, Sender: protocol.Identity{Nick: "host"}, Body: grant}
		}
		return client
	}
	nick, pass, err := requestGrant(context.Background(), newClient, testInvite(), time.Second)
	if err != nil || nick != "g1" || pass != "good" {
		t.Fatalf("RequestGrant = %q, %q, %v", nick, pass, err)
	}
}

func TestRequestGrantDeny(t *testing.T) {
	newClient := func(config irc.RealConfig) Client {
		client := &fakeIRC{config: config, events: make(chan protocol.InboundEvent, 1)}
		client.onSend = func(msg protocol.OutboundMessage) {
			req, _ := joinproto.ParseJoin(msg.Body)
			client.events <- protocol.InboundEvent{ID: "deny", Kind: protocol.KindDM, Sender: protocol.Identity{Nick: "host"}, Body: joinproto.FormatDeny(req.Nonce, "registration unavailable")}
		}
		return client
	}
	_, _, err := requestGrant(context.Background(), newClient, testInvite(), time.Second)
	if err == nil || !strings.Contains(err.Error(), "registration unavailable") {
		t.Fatalf("RequestGrant error = %v", err)
	}
}

func TestRequestGrantTimeout(t *testing.T) {
	newClient := func(config irc.RealConfig) Client {
		return &fakeIRC{config: config, events: make(chan protocol.InboundEvent)}
	}
	_, _, err := requestGrant(context.Background(), newClient, testInvite(), 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "host agent did not answer") {
		t.Fatalf("RequestGrant error = %v", err)
	}
}

func TestRequestGrantSendFailureDisconnects(t *testing.T) {
	newClient := func(config irc.RealConfig) Client {
		return &sendErrorClient{fakeIRC: fakeIRC{config: config, events: make(chan protocol.InboundEvent)}, err: fmt.Errorf("send failed")}
	}
	if _, _, err := requestGrant(context.Background(), newClient, testInvite(), time.Second); err == nil || !strings.Contains(err.Error(), "send failed") {
		t.Fatalf("RequestGrant error = %v", err)
	}
}

type sendErrorClient struct {
	fakeIRC
	err error
}

func (f *sendErrorClient) Send(protocol.OutboundMessage) error { return f.err }

func testInvite() invite.Invite {
	return invite.Invite{Host: "127.0.0.1", Port: 6697, JoinNick: "join", JoinPassword: "join-password", Peer: "host", FP: "fp"}
}
