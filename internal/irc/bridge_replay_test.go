package irc_test

import (
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

func TestBridgeDropsReplayedEventID(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	first := dmToAgent("evt-1", "alice", "hi")
	if err := br.Deliver(first); err != nil {
		t.Fatal(err)
	}
	// The same event arrives again after a reconnect.
	if err := br.Deliver(first); err == nil {
		t.Fatal("replayed event ID must be dropped")
	}
	got, ok := svc.Next("agent-a")
	if !ok || got.ID != "evt-1" {
		t.Fatalf("only one delivery expected, got %v ok=%v", got, ok)
	}
	if _, ok := svc.Next("agent-a"); ok {
		t.Fatal("replay must not create a second delivery")
	}
}

func TestBridgeDropsTimestampReplay(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	fresh := dmToAgent("evt-1", "alice", "fresh")
	fresh.SentAt = 100
	if err := br.Deliver(fresh); err != nil {
		t.Fatal(err)
	}
	// A different ID but an older timestamp is history, not new work.
	stale := dmToAgent("evt-old", "alice", "stale")
	stale.SentAt = 50
	if err := br.Deliver(stale); err == nil {
		t.Fatal("older-than-cursor event must be dropped")
	}
	// Only the fresh event is queued.
	got, ok := svc.Next("agent-a")
	if !ok || got.Body != "fresh" {
		t.Fatalf("want the fresh event, got %v ok=%v", got, ok)
	}
	if _, ok := svc.Next("agent-a"); ok {
		t.Fatalf("replayed event must not create a second delivery")
	}
}

func TestBridgeAcceptsNewerEventAfterReplay(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	first := dmToAgent("evt-1", "alice", "first")
	first.SentAt = 100
	if err := br.Deliver(first); err != nil {
		t.Fatal(err)
	}
	second := dmToAgent("evt-2", "alice", "second")
	second.SentAt = 200
	if err := br.Deliver(second); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for {
		ev, ok := svc.Next("agent-a")
		if !ok {
			break
		}
		bodies = append(bodies, ev.Body)
	}
	if len(bodies) != 2 || bodies[0] != "first" || bodies[1] != "second" {
		t.Fatalf("want first then second, got %v", bodies)
	}
}

func TestBridgeCursorIsPerEndpoint(t *testing.T) {
	t.Parallel()
	snd := &recordSender{}
	svc := core.NewService(snd, syncClock{})
	for _, id := range []protocol.EndpointID{"agent-a", "agent-b"} {
		if err := svc.Register(core.Endpoint{ID: id, Harness: "pi", SessionID: "ses", Nick: string(id)}); err != nil {
			t.Fatal(err)
		}
		if err := svc.Enable(id); err != nil {
			t.Fatal(err)
		}
	}
	br := irc.NewBridge(svc)

	ev := protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "alice"}, Body: "hi", SentAt: 100,
	}
	if err := br.Deliver(ev); err != nil {
		t.Fatal(err)
	}
	// The same ID is fresh work for a different endpoint.
	other := ev
	other.EndpointID = "agent-b"
	other.Target = "agent-b"
	if err := br.Deliver(other); err != nil {
		t.Fatalf("same ID on a different endpoint is not a replay: %v", err)
	}
	if _, ok := svc.Next("agent-a"); !ok {
		t.Fatal("agent-a should have its event")
	}
	if _, ok := svc.Next("agent-b"); !ok {
		t.Fatal("agent-b should have its own event")
	}
}
