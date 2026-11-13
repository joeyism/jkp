package core_test

import (
	"fmt"
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

func evWithID(id string) protocol.InboundEvent {
	return protocol.InboundEvent{
		ID: id, EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "alice"}, Body: id,
	}
}

func TestInboxFIFO(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	if err := in.Enqueue(evWithID("a")); err != nil {
		t.Fatal(err)
	}
	if err := in.Enqueue(evWithID("b")); err != nil {
		t.Fatal(err)
	}
	got, ok := in.Dequeue()
	if !ok || got.ID != "a" {
		t.Fatalf("want a, got %v ok=%v", got, ok)
	}
	got, ok = in.Dequeue()
	if !ok || got.ID != "b" {
		t.Fatalf("want b, got %v ok=%v", got, ok)
	}
	if _, ok := in.Dequeue(); ok {
		t.Fatal("inbox should be empty")
	}
}

func TestInboxDuplicateIgnored(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	if err := in.Enqueue(evWithID("dup")); err != nil {
		t.Fatal(err)
	}
	if err := in.Enqueue(evWithID("dup")); err != core.ErrDuplicate {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
	if in.Len() != 1 {
		t.Fatalf("want len 1, got %d", in.Len())
	}
}

func TestInboxQueueFull(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	for i := 0; i < 8; i++ {
		if err := in.Enqueue(evWithID(fmt.Sprintf("e%d", i))); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	if err := in.Enqueue(evWithID("overflow")); err != core.ErrQueueFull {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if in.Len() != 8 {
		t.Fatalf("want len 8, got %d", in.Len())
	}
}

func TestInboxDropStaleGeneration(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	if err := in.Enqueue(evWithID("stale")); err != nil {
		t.Fatal(err)
	}
	in.Disable() // bumps generation fence
	if _, ok := in.Dequeue(); ok {
		t.Fatal("stale generation must not dequeue after disable")
	}
}

func TestInboxAcceptsAfterReEnable(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	if err := in.Enqueue(evWithID("stale")); err != nil {
		t.Fatal(err)
	}
	in.Disable()
	if err := in.Enable(); err != nil {
		t.Fatal(err)
	}
	// Stale item is gone; new work still admitted.
	if err := in.Enqueue(evWithID("fresh")); err != nil {
		t.Fatal(err)
	}
	got, ok := in.Dequeue()
	if !ok || got.ID != "fresh" {
		t.Fatalf("want fresh only, got %v ok=%v", got, ok)
	}
}
