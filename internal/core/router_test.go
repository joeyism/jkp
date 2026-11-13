package core_test

import (
	"sync"
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

// fakeSender records outbound sends without touching IRC.
type fakeSender struct {
	mu   sync.Mutex
	sent []protocol.OutboundMessage
	err  error
}

func (f *fakeSender) Send(msg protocol.OutboundMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeSender) messages() []protocol.OutboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.OutboundMessage, len(f.sent))
	copy(out, f.sent)
	return out
}

func newRouterWithSender(t *testing.T) (*core.Router, *fakeSender) {
	t.Helper()
	s := &fakeSender{}
	r := core.NewRouter(s)
	if err := r.Register("agent-a", "agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("agent-b", "agent-b"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("agent-b"); err != nil {
		t.Fatal(err)
	}
	return r, s
}

func dmFrom(sender string) protocol.InboundEvent {
	return protocol.InboundEvent{
		ID: "evt-" + sender, EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: sender}, Body: "hi",
	}
}

func TestReplyToDMTargetsOriginalSender(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "pushback", InReplyTo: "evt-alice",
	}
	if err := r.Reply(out); err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 {
		t.Fatalf("want 1 send, got %d", len(got))
	}
	if got[0].Kind != protocol.KindDM || got[0].Target != "alice" {
		t.Fatalf("reply must target the original DM sender, got %v", got[0])
	}
}

func TestReplyToRoomTargetsOriginalChannel(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	ev := protocol.InboundEvent{
		ID: "evt-r", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Sender: protocol.Identity{Account: "alice"},
		Body: "agent-a look", Mentions: []string{"agent-a"},
	}
	if err := r.Admit(ev); err != nil {
		t.Fatal(err)
	}
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Body: "ack", InReplyTo: "evt-r",
	}
	if err := r.Reply(out); err != nil {
		t.Fatal(err)
	}
	if got := s.messages(); got[0].Target != "#lab" {
		t.Fatalf("room reply must target #lab, got %s", got[0].Target)
	}
}

func TestReplyTargetMismatchRejected(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	// Reply claims a room target while answering a DM.
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Body: "leak?", InReplyTo: "evt-alice",
	}
	if err := r.Reply(out); err != core.ErrTargetMismatch {
		t.Fatalf("want ErrTargetMismatch, got %v", err)
	}
	if len(s.messages()) != 0 {
		t.Fatal("mismatched reply must not be sent")
	}
}

func TestReplyKindMismatchRejected(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "x", InReplyTo: "evt-alice",
	}
	// Corrupt kind to room while keeping the DM target.
	out.Kind = protocol.KindRoom
	if err := r.Reply(out); err != core.ErrTargetMismatch {
		t.Fatalf("want ErrTargetMismatch for kind drift, got %v", err)
	}
	if len(s.messages()) != 0 {
		t.Fatal("kind-mismatched reply must not be sent")
	}
}

func TestReplyToUnknownEventRejected(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "x", InReplyTo: "evt-nope",
	}
	if err := r.Reply(out); err != core.ErrUnknownEvent {
		t.Fatalf("want ErrUnknownEvent, got %v", err)
	}
	if len(s.messages()) != 0 {
		t.Fatal("nothing may be sent for an unknown event")
	}
}

func TestReplyFromWrongEndpointRejected(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	// agent-b tries to answer agent-a's event.
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-b", Kind: protocol.KindDM,
		Target: "alice", Body: "hijack", InReplyTo: "evt-alice",
	}
	if err := r.Reply(out); err != core.ErrWrongEndpoint {
		t.Fatalf("want ErrWrongEndpoint, got %v", err)
	}
	if len(s.messages()) != 0 {
		t.Fatal("cross-endpoint reply must not be sent")
	}
}

func TestReplyAfterDisableRejected(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	if err := r.Disable("agent-a"); err != nil {
		t.Fatal(err)
	}
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "late", InReplyTo: "evt-alice",
	}
	if err := r.Reply(out); err != core.ErrStaleGeneration {
		t.Fatalf("want ErrStaleGeneration, got %v", err)
	}
	if len(s.messages()) != 0 {
		t.Fatal("late reply after disable must not be sent")
	}
}

func TestTwoEndpointsCannotReadEachOthersInbound(t *testing.T) {
	t.Parallel()
	r, _ := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	// agent-a's own queue has the event.
	if _, ok := r.Next("agent-a"); !ok {
		t.Fatal("agent-a should see its own event")
	}
	// agent-b must see nothing.
	if _, ok := r.Next("agent-b"); ok {
		t.Fatal("agent-b must not read agent-a's inbound stream")
	}
}

func TestInterleavedEventsDoNotCrossRouteReplies(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	dm := dmFrom("alice")
	dm.ID = "evt-dm"
	if err := r.Admit(dm); err != nil {
		t.Fatal(err)
	}
	room := protocol.InboundEvent{
		ID: "evt-room", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Sender: protocol.Identity{Account: "bob"},
		Body: "agent-a question", Mentions: []string{"agent-a"},
	}
	if err := r.Admit(room); err != nil {
		t.Fatal(err)
	}

	// Reply to the room first, then the DM; targets must stay correct.
	if err := r.Reply(protocol.OutboundMessage{
		ID: "out-r", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Body: "room answer", InReplyTo: "evt-room",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Reply(protocol.OutboundMessage{
		ID: "out-d", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "dm answer", InReplyTo: "evt-dm",
	}); err != nil {
		t.Fatal(err)
	}

	got := s.messages()
	if len(got) != 2 {
		t.Fatalf("want 2 sends, got %d", len(got))
	}
	if got[0].Target != "#lab" || got[1].Target != "alice" {
		t.Fatalf("cross-routed replies: %+v", got)
	}
}
