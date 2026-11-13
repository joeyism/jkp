package irc_test

import (
	"testing"
	"time"

	"github.com/joey/jkp/internal/irc"
)

// TestAttachReplaceDoesNotDeadlock covers a real bug: replacing a transport
// while a previous one is attached used to deadlock because the stop function
// re-acquired the bridge mutex that Attach already held. Go mutexes are not
// reentrant.
func TestAttachReplaceDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	svc, br, _ := newBridgeFixture(t)
	_ = svc

	first := irc.NewFakeClient("agent-a")
	_ = first.Connect(t.Context())
	stop1 := br.Attach("agent-a", first)

	done := make(chan struct{})
	go func() {
		defer close(done)
		second := irc.NewFakeClient("agent-a")
		_ = second.Connect(t.Context())
		stop2 := br.Attach("agent-a", second)
		stop2()
	}()

	select {
	case <-done:
		// Replacing the transport completed without deadlocking.
	case <-time.After(3 * time.Second):
		t.Fatal("Attach deadlocked replacing an existing transport")
	}
	stop1()
}
