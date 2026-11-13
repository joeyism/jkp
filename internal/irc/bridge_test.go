package irc_test

import (
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

type syncClock struct{}

func (syncClock) Now() time.Time { return time.Now() }

type recordSender struct{ sent []protocol.OutboundMessage }

func (r *recordSender) Send(msg protocol.OutboundMessage) error {
	r.sent = append(r.sent, msg)
	return nil
}

func newBridgeFixture(t *testing.T) (*core.Service, *irc.Bridge, *recordSender) {
	t.Helper()
	snd := &recordSender{}
	svc := core.NewService(snd, syncClock{})
	if err := svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "ses_a", Nick: "agent-a"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Enable("agent-a"); err != nil {
		t.Fatal(err)
	}
	return svc, irc.NewBridge(svc), snd
}

func dmToAgent(id, sender, body string) protocol.InboundEvent {
	return protocol.InboundEvent{
		ID: id, EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: sender}, Body: body,
	}
}

func TestBridgeDeliversDMToService(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	if err := br.Deliver(dmToAgent("evt-1", "alice", "hi")); err != nil {
		t.Fatal(err)
	}
	got, ok := svc.Next("agent-a")
	if !ok || got.ID != "evt-1" {
		t.Fatalf("want evt-1 delivered, got %v ok=%v", got, ok)
	}
}

func TestBridgeDropsUnaddressedRoomTraffic(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	ev := protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Sender: protocol.Identity{Account: "bob"}, Body: "general chatter",
	}
	if err := br.Deliver(ev); err == nil {
		t.Fatal("unaddressed room traffic must be dropped")
	}
	if _, ok := svc.Next("agent-a"); ok {
		t.Fatal("no delivery may be created for unaddressed room traffic")
	}
}

func TestBridgeDropsUnknownEndpoint(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	ev := dmToAgent("evt-1", "alice", "hi")
	ev.EndpointID = "nobody"
	if err := br.Deliver(ev); err == nil {
		t.Fatal("events for unknown endpoints must be dropped")
	}
	if _, ok := svc.Next("agent-a"); ok {
		t.Fatal("no delivery for an unknown endpoint")
	}
}

func TestBridgeReplyGoesToOriginalSender(t *testing.T) {
	t.Parallel()
	svc, br, snd := newBridgeFixture(t)
	if err := br.Deliver(dmToAgent("evt-1", "alice", "hi")); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Next("agent-a")

	if err := svc.Reply(protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: got.Kind,
		Target: got.ReplyTarget(), Body: "pong", InReplyTo: got.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(snd.sent) != 1 || snd.sent[0].Target != "alice" {
		t.Fatalf("reply must reach the original sender, got %+v", snd.sent)
	}
}

func TestBridgeDropsAfterDisable(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	if err := svc.Disable("agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := br.Deliver(dmToAgent("evt-1", "alice", "hi")); err == nil {
		t.Fatal("disabled endpoint must reject transport events")
	}
	if _, ok := svc.Next("agent-a"); ok {
		t.Fatal("no delivery after disable")
	}
}

func TestBridgeAttachPumpsEvents(t *testing.T) {
	svc, br, _ := newBridgeFixture(t)
	c := irc.NewFakeClient("agent-a")
	if err := c.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	stop := br.Attach("agent-a", c)
	defer stop()

	c.InjectDM("alice", "alice", "pumped")

	deadline := time.After(2 * time.Second)
	for {
		if ev, ok := svc.Next("agent-a"); ok {
			if ev.Body != "pumped" {
				t.Fatalf("unexpected event %+v", ev)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("bridge pump did not deliver the injected event")
		case <-time.After(2 * time.Millisecond):
		}
	}
}
