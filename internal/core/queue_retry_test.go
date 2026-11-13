package core_test

import (
	"testing"

	"github.com/joey/jkp/internal/core"
)

func TestInboxRetrySameIDAcceptedAfterDisable(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	if err := in.Enqueue(evWithID("retried")); err != nil {
		t.Fatal(err)
	}
	in.Disable()
	if err := in.Enable(); err != nil {
		t.Fatal(err)
	}
	// Dropped-but-never-dequeued work may be retried under the same ID; this
	// is a fresh generation and the event was never consumed by a turn.
	if err := in.Enqueue(evWithID("retried")); err != nil {
		t.Fatalf("retry after disable should be accepted, got %v", err)
	}
	got, ok := in.Dequeue()
	if !ok || got.ID != "retried" {
		t.Fatalf("want retried, got %v ok=%v", got, ok)
	}
}

func TestInboxGenerationAdvancesOnDisable(t *testing.T) {
	t.Parallel()
	in := core.NewInbox(8)
	before := in.Generation()
	in.Disable()
	if in.Generation() != before+1 {
		t.Fatalf("generation must advance on disable")
	}
}
