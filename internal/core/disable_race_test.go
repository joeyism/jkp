package core_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

func TestDisableDropsQueuedEvent(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	// Fill the first turn so the second event is queued, not delivered.
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	queued := dmFrom("bob")
	queued.ID = "evt-queued"
	if err := r.Admit(queued); err != nil {
		t.Fatal(err)
	}
	if err := r.Disable("agent-a"); err != nil {
		t.Fatal(err)
	}
	// Drain everything that remains; the queued event must be gone.
	for {
		if _, ok := r.Next("agent-a"); !ok {
			break
		}
	}
	// A late reply for the queued event is fenced too.
	out := protocol.OutboundMessage{
		ID: "out-q", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "bob", Body: "late", InReplyTo: "evt-queued",
	}
	if err := r.Reply(out); err == nil {
		t.Fatal("late reply for dropped work must be rejected")
	}
	if len(s.messages()) != 0 {
		t.Fatal("nothing may be sent for dropped work")
	}
}

func TestDisableFencesInFlightReply(t *testing.T) {
	t.Parallel()
	r, s := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	// Model is mid-turn (Busy) when the owner disables.
	if err := r.Disable("agent-a"); err != nil {
		t.Fatal(err)
	}
	out := protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "too late", InReplyTo: "evt-alice",
	}
	if err := r.Reply(out); err != core.ErrStaleGeneration {
		t.Fatalf("want ErrStaleGeneration, got %v", err)
	}
	if len(s.messages()) != 0 {
		t.Fatal("in-flight reply after disable must not reach IRC")
	}
}

func TestReEnableDoesNotReplayDroppedWork(t *testing.T) {
	t.Parallel()
	r, _ := newRouterWithSender(t)
	if err := r.Admit(dmFrom("alice")); err != nil {
		t.Fatal(err)
	}
	queued := dmFrom("bob")
	queued.ID = "evt-queued"
	if err := r.Admit(queued); err != nil {
		t.Fatal(err)
	}
	if err := r.Disable("agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("agent-a"); err != nil {
		t.Fatal(err)
	}
	// Nothing queued under the old generation may surface as new work.
	for {
		ev, ok := r.Next("agent-a")
		if !ok {
			break
		}
		t.Fatalf("re-enable must not replay %q as new work", ev.ID)
	}
}

// TestConcurrentAdmitReplyDisableIsAtomic drives Admit/Reply/Disable from many
// goroutines and asserts the router's state transitions stay atomic: every
// Reply either sends exactly once or fails with an error, and after Disable
// returns no further send is possible. The -race flag is the primary check.
func TestConcurrentAdmitReplyDisableIsAtomic(t *testing.T) {
	t.Parallel()
	s := &countingSender{}
	r := core.NewRouter(s)

	if err := r.Register("agent-a", "agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("agent-a"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "evt-" + strconv.Itoa(i)
			ev := protocol.InboundEvent{
				ID: id, EndpointID: "agent-a", Kind: protocol.KindDM,
				Target: "agent-a", Sender: protocol.Identity{Account: "alice"},
				Body: "hi",
			}
			_ = r.Admit(ev)
			_ = r.Reply(protocol.OutboundMessage{
				ID: "out-" + strconv.Itoa(i), EndpointID: "agent-a",
				Kind: protocol.KindDM, Target: "alice",
				Body: "reply", InReplyTo: id,
			})
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = r.Disable("agent-a")
	}()
	wg.Wait()

	// Disable has returned: no send may happen from here on.
	before := s.count()
	if err := r.Reply(protocol.OutboundMessage{
		ID: "out-final", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "alice", Body: "late", InReplyTo: "evt-0",
	}); err == nil {
		t.Fatal("reply after disable must fail")
	}
	if s.count() != before {
		t.Fatal("a send occurred after Disable returned")
	}
}

// countingSender records sends. A reply that returns nil must have sent exactly
// once; that pairing is what the concurrency test verifies is never split.
type countingSender struct {
	mu    sync.Mutex
	sends int
}

func (s *countingSender) Send(protocol.OutboundMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	return nil
}

func (s *countingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends
}
