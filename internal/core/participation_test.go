package core_test

import (
	"testing"

	"github.com/joey/jkp/internal/core"
)

func TestEnableMovesDisabledToAccepting(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	if err := p.Enable(); err != nil {
		t.Fatal(err)
	}
	if p.State() != core.Accepting {
		t.Fatalf("got %s", p.State())
	}
}

func TestDisableFromAnyState(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	_ = p.Enable()
	_ = p.BeginTurn()
	if err := p.Disable(); err != nil {
		t.Fatal(err)
	}
	if p.State() != core.Disabled {
		t.Fatalf("got %s", p.State())
	}
}

func TestBeginTurnFromDisabledFails(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	if err := p.BeginTurn(); err == nil {
		t.Fatal("expected error")
	}
}

func TestEndTurnFromBusyReturnsAccepting(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	_ = p.Enable()
	_ = p.BeginTurn()
	if err := p.EndTurn(); err != nil {
		t.Fatal(err)
	}
	if p.State() != core.Accepting {
		t.Fatalf("got %s", p.State())
	}
}

func TestEnableIsIdempotent(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	_ = p.Enable()
	if err := p.Enable(); err != nil {
		t.Fatal(err)
	}
}

func TestBeginTurnWhileBusyFails(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	_ = p.Enable()
	_ = p.BeginTurn()
	if err := p.BeginTurn(); err == nil {
		t.Fatal("second turn while busy must fail")
	}
}

func TestEndTurnFromDisabledFails(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	if err := p.EndTurn(); err == nil {
		t.Fatal("ending a turn while disabled must fail")
	}
}
