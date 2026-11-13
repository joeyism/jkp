package irc_test

import (
	"context"
	"testing"

	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

func TestFakeInjectDMEmitsEvent(t *testing.T) {
	t.Parallel()
	c := irc.NewFakeClient("agent-a")
	_ = c.Connect(context.Background())

	c.InjectDM("alice", "alice", "hello")
	ev := <-c.Events()
	if ev.Kind != protocol.KindDM || ev.Sender.Account != "alice" || ev.Body != "hello" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("fake must emit valid events: %v", err)
	}
}

func TestFakeInjectRoomEmitsEventWithMentions(t *testing.T) {
	t.Parallel()
	c := irc.NewFakeClient("agent-a")
	_ = c.Connect(context.Background())

	c.InjectRoom("#lab", "bob", "agent-a look", []string{"agent-a"})
	ev := <-c.Events()
	if ev.Kind != protocol.KindRoom || ev.Target != "#lab" {
		t.Fatalf("unexpected room event: %+v", ev)
	}
	if len(ev.Mentions) != 1 || ev.Mentions[0] != "agent-a" {
		t.Fatalf("mentions not preserved: %+v", ev.Mentions)
	}
}

func TestFakeSendRecordsOutbound(t *testing.T) {
	t.Parallel()
	c := irc.NewFakeClient("agent-a")
	_ = c.Connect(context.Background())

	msg := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "pong", InReplyTo: "evt-1",
	}
	if err := c.Send(msg); err != nil {
		t.Fatal(err)
	}
	sent := c.Sent()
	if len(sent) != 1 || sent[0].Target != "alice" {
		t.Fatalf("want one send to alice, got %+v", sent)
	}
}

func TestFakeDisconnectClosesEvents(t *testing.T) {
	t.Parallel()
	c := irc.NewFakeClient("agent-a")
	_ = c.Connect(context.Background())
	_ = c.Disconnect()
	if _, open := <-c.Events(); open {
		t.Fatal("Events must be closed after Disconnect")
	}
}

func TestFakeUnauthenticatedSender(t *testing.T) {
	t.Parallel()
	c := irc.NewFakeClient("agent-a")
	_ = c.Connect(context.Background())

	// Nick-only identity: no SASL account.
	c.InjectDM("", "mallory", "hi")
	ev := <-c.Events()
	if ev.Sender.Authenticated() {
		t.Fatal("nick without account must not be authenticated")
	}
	if ev.Sender.Nick != "mallory" {
		t.Fatalf("nick must be preserved: %+v", ev.Sender)
	}
}

func TestFakeSendAfterDisconnectFails(t *testing.T) {
	t.Parallel()
	c := irc.NewFakeClient("agent-a")
	_ = c.Connect(context.Background())
	_ = c.Disconnect()
	err := c.Send(protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "x", InReplyTo: "evt-1",
	})
	if err == nil {
		t.Fatal("send after disconnect must fail")
	}
}
