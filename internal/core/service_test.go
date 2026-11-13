package core_test

import (
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func newService(t *testing.T) (*core.Service, *fakeSender) {
	t.Helper()
	s := &fakeSender{}
	svc := core.NewService(s, realClock{})
	mustRegister(t, svc, core.Endpoint{ID: "agent-a", Harness: "opencode", SessionID: "ses_a", Nick: "agent-a"})
	mustRegister(t, svc, core.Endpoint{ID: "agent-b", Harness: "pi", SessionID: "ses_b", Nick: "agent-b"})
	for _, id := range []protocol.EndpointID{"agent-a", "agent-b"} {
		if err := svc.Enable(id); err != nil {
			t.Fatal(err)
		}
	}
	return svc, s
}

func mustRegister(t *testing.T, svc *core.Service, ep core.Endpoint) {
	t.Helper()
	if err := svc.Register(ep); err != nil {
		t.Fatal(err)
	}
}

func TestServiceFullLoop(t *testing.T) {
	t.Parallel()
	svc, s := newService(t)

	ev := protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "agent-b"}, Body: "hi",
	}
	if err := svc.Admit(ev); err != nil {
		t.Fatal(err)
	}
	got, ok := svc.Next("agent-a")
	if !ok || got.ID != "evt-1" {
		t.Fatalf("want evt-1, got %v ok=%v", got, ok)
	}

	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-b", Body: "pong", InReplyTo: "evt-1",
	}
	if err := svc.Reply(out); err != nil {
		t.Fatal(err)
	}
	msgs := s.messages()
	if len(msgs) != 1 || msgs[0].Target != "agent-b" {
		t.Fatalf("want one send to agent-b, got %+v", msgs)
	}

	// Finish the conversation; further admissions on it fail.
	conv := protocol.Conversation{Kind: protocol.KindDM, Target: "agent-b"}
	if err := svc.Finish("agent-a", conv); err != nil {
		t.Fatal(err)
	}
	err := svc.Admit(protocol.InboundEvent{
		ID: "evt-2", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "agent-b"}, Body: "again",
	})
	if err != core.ErrConversationFinished {
		t.Fatalf("want ErrConversationFinished, got %v", err)
	}
}

func TestServiceSeparatesConversationsPerPeer(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	convA := protocol.Conversation{Kind: protocol.KindDM, Target: "agent-b"}
	convB := protocol.Conversation{Kind: protocol.KindDM, Target: "human"}

	if err := svc.Finish("agent-a", convA); err != nil {
		t.Fatal(err)
	}
	// A different peer's conversation is unaffected.
	if err := svc.Admit(protocol.InboundEvent{
		ID: "evt-h", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "human"}, Body: "question",
	}); err != nil {
		t.Fatalf("independent conversation must admit, got %v", err)
	}
	_ = convB
}

func TestServiceQueuesConcurrentEventsFromSameDMPeer(t *testing.T) {
	svc, _ := newService(t)
	for _, id := range []string{"evt-a", "evt-b"} {
		if err := svc.Admit(protocol.InboundEvent{
			ID: id, EndpointID: "agent-a", Kind: protocol.KindDM,
			Target: "agent-a", Sender: protocol.Identity{Nick: "agent-b"}, Body: "join request",
		}); err != nil {
			t.Fatalf("Admit(%s): %v", id, err)
		}
	}
	for _, want := range []string{"evt-a", "evt-b"} {
		got, ok := svc.Next("agent-a")
		if !ok || got.ID != want {
			t.Fatalf("Next = %#v, %v; want %s", got, ok, want)
		}
	}
}

func TestServiceQueuesSamePeerEventArrivingWhileFirstIsBeingHandled(t *testing.T) {
	svc, _ := newService(t)
	newEvent := func(id string) protocol.InboundEvent {
		return protocol.InboundEvent{ID: id, EndpointID: "agent-a", Kind: protocol.KindDM, Target: "agent-a", Sender: protocol.Identity{Nick: "agent-b"}, Body: "join request"}
	}
	if err := svc.Admit(newEvent("evt-a")); err != nil {
		t.Fatal(err)
	}
	if got, ok := svc.Next("agent-a"); !ok || got.ID != "evt-a" {
		t.Fatalf("first Next = %#v %v", got, ok)
	}
	if err := svc.Admit(newEvent("evt-b")); err != nil {
		t.Fatalf("Admit second event while first turn is busy: %v", err)
	}
	if got, ok := svc.Next("agent-a"); !ok || got.ID != "evt-b" {
		t.Fatalf("second Next = %#v %v, want evt-b", got, ok)
	}
}

func TestServiceDisableRejectsAdmission(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	if err := svc.Disable("agent-a"); err != nil {
		t.Fatal(err)
	}
	err := svc.Admit(protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "human"}, Body: "hi",
	})
	if err != core.ErrNotAccepting {
		t.Fatalf("want ErrNotAccepting, got %v", err)
	}
}

func TestServiceListEndpoints(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	eps := svc.List()
	if len(eps) != 2 {
		t.Fatalf("want 2 endpoints, got %d", len(eps))
	}
	found := false
	for _, ep := range eps {
		if ep.ID == "agent-a" && ep.SessionID == "ses_a" {
			found = true
		}
	}
	if !found {
		t.Fatal("agent-a with ses_a must be listed")
	}
}

func TestServiceUnknownEndpointNext(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	if _, ok := svc.Next("nobody"); ok {
		t.Fatal("unknown endpoint must have no delivery")
	}
}

func TestServiceResetConversationStartsNewVolley(t *testing.T) {
	sender := &fakeSender{}
	svc := core.NewServiceWithBounds(sender, realClock{}, core.BoundsConfig{MaxTurns: 1, MaxAge: 0})
	mustRegister(t, svc, core.Endpoint{ID: "agent-a", Harness: "opencode", SessionID: "s", Nick: "agent-a"})
	if err := svc.Enable("agent-a"); err != nil {
		t.Fatal(err)
	}
	newEvent := func(id string, humanInitiated bool) protocol.InboundEvent {
		return protocol.InboundEvent{
			ID: id, EndpointID: "agent-a", Kind: protocol.KindDM,
			Target: "agent-a", Sender: protocol.Identity{Nick: "alice"}, Body: "hello", HumanInitiated: humanInitiated,
		}
	}
	if err := svc.Admit(newEvent("evt-1", false)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Admit(newEvent("evt-2", false)); err != core.ErrTooManyTurns {
		t.Fatalf("want turn cap before reset, got %v", err)
	}
	if err := svc.Admit(newEvent("evt-3", true)); err != nil {
		t.Fatalf("human-initiated message should reset receiver volley: %v", err)
	}
}
