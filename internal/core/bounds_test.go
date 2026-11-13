package core_test

import (
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func conv(target string) protocol.Conversation {
	return protocol.Conversation{Kind: protocol.KindDM, Target: target}
}

func TestConversationTurnLimit(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	c := conv("alice")
	for i := 0; i < core.MaxAutonomousTurns; i++ {
		if err := b.Admit(c); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	if err := b.Admit(c); err != core.ErrTooManyTurns {
		t.Fatalf("want ErrTooManyTurns, got %v", err)
	}
}

func TestConversationExpires(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(0, 0)}
	b := core.NewBounds(clk)
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	clk.advance(core.MaxAutonomousAge + time.Minute)
	if err := b.Admit(c); err != core.ErrConversationExpired {
		t.Fatalf("want ErrConversationExpired, got %v", err)
	}
}

func TestConversationNotExpiredAtBoundary(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(0, 0)}
	b := core.NewBounds(clk)
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	clk.advance(core.MaxAutonomousAge)
	if err := b.Admit(c); err != nil {
		t.Fatalf("exactly at the limit should still admit, got %v", err)
	}
}

func TestFinishedRejectsFurtherAdmission(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(c); err != nil {
		t.Fatal(err)
	}
	if err := b.Admit(c); err != core.ErrConversationFinished {
		t.Fatalf("want ErrConversationFinished, got %v", err)
	}
}

func TestFinishIsIdempotent(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(c); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(c); err != nil {
		t.Fatalf("repeated finish must be harmless, got %v", err)
	}
}

func TestDisconnectWithoutFinishIsIncomplete(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	b.Disconnect(c)
	if got := b.Status(c); got != core.StatusIncomplete {
		t.Fatalf("want Incomplete, got %s", got)
	}
}

func TestFinishedIsFinished(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(c); err != nil {
		t.Fatal(err)
	}
	if got := b.Status(c); got != core.StatusFinished {
		t.Fatalf("want Finished, got %s", got)
	}
}

func TestDisconnectAfterFinishStaysFinished(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	c := conv("alice")
	if err := b.Admit(c); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(c); err != nil {
		t.Fatal(err)
	}
	b.Disconnect(c)
	if got := b.Status(c); got != core.StatusFinished {
		t.Fatalf("want Finished, got %s", got)
	}
}

func TestConversationsAreIndependent(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	a := conv("alice")
	bo := conv("bob")
	for i := 0; i < core.MaxAutonomousTurns; i++ {
		if err := b.Admit(a); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Admit(a); err != core.ErrTooManyTurns {
		t.Fatalf("conversation a must hit its own limit, got %v", err)
	}
	if err := b.Admit(bo); err != nil {
		t.Fatalf("conversation b is independent and must admit, got %v", err)
	}
}

func TestUnknownConversationStatus(t *testing.T) {
	t.Parallel()
	b := core.NewBounds(&fakeClock{t: time.Unix(0, 0)})
	if got := b.Status(conv("nobody")); got != core.StatusUnknown {
		t.Fatalf("want Unknown, got %s", got)
	}
}

func TestRoomBoundsResetTurnCapAfterIdleWithoutWallClockExpiry(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(0, 0)}
	b := core.NewBoundsWithConfig(clk, core.BoundsConfig{
		MaxTurns:  core.MaxAutonomousTurns,
		MaxAge:    0,
		IdleReset: 5 * time.Minute,
	})
	c := conv("alice")
	for i := 0; i < core.MaxAutonomousTurns; i++ {
		clk.advance(10 * time.Second)
		if err := b.Admit(c); err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
	}
	if err := b.Admit(c); err != core.ErrTooManyTurns {
		t.Fatalf("rapid volley should stop at the cap, got %v", err)
	}

	clk.advance(2 * time.Hour)
	if err := b.Admit(c); err != nil {
		t.Fatalf("persistent room should admit a new volley after idle: %v", err)
	}
	if got := b.Turns(c); got != 1 {
		t.Fatalf("turn count after idle = %d, want 1", got)
	}
}

func TestResetStartsNewVolleyWithoutFinishingConversation(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{t: time.Unix(0, 0)}
	b := core.NewBoundsWithConfig(clk, core.BoundsConfig{MaxTurns: 2, MaxAge: 0})
	c := conv("alice")
	for i := 0; i < 2; i++ {
		if err := b.Admit(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Admit(c); err != core.ErrTooManyTurns {
		t.Fatalf("want turn cap, got %v", err)
	}
	b.Reset(c)
	if err := b.Admit(c); err != nil {
		t.Fatalf("Admit after Reset: %v", err)
	}
	if got := b.Status(c); got != core.StatusActive {
		t.Fatalf("status after reset = %s, want active", got)
	}
}
