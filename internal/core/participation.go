package core

import (
	"errors"
	"sync"
)

// State is an endpoint's participation state. It lives entirely in the core;
// the model cannot move it and IRC reconnects cannot silently reset it.
type State string

const (
	// Disabled rejects all admissions and outbound replies.
	Disabled State = "disabled"
	// Accepting admits inbound events and starts turns.
	Accepting State = "accepting"
	// Busy queues inbound events instead of starting overlapping turns.
	Busy State = "busy"
)

// Errors returned by participation transitions.
var (
	ErrNotAccepting = errors.New("not accepting")
	ErrNotBusy      = errors.New("not busy")
)

// Participation is a concurrency-safe state machine for one endpoint.
// Generation increments on every Disable so stale queued work can be fenced.
type Participation struct {
	mu    sync.Mutex
	state State
	gen   uint64
}

// NewParticipation returns a disabled endpoint state machine.
func NewParticipation() *Participation {
	return &Participation{state: Disabled}
}

// State reports the current state.
func (p *Participation) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Generation reports the current fencing generation. It is bumped by Disable.
func (p *Participation) Generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gen
}

// Enable moves Disabled -> Accepting, or leaves an already accepting/busy
// endpoint alone. Enabling never replays work queued under a previous
// generation.
func (p *Participation) Enable() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == Disabled {
		p.state = Accepting
	}
	return nil
}

// Disable moves to Disabled and bumps the generation fence. It always
// succeeds. Work queued under an older generation must be dropped.
func (p *Participation) Disable() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = Disabled
	p.gen++
	return nil
}

// BeginTurn moves Accepting -> Busy. It fails from Disabled or Busy so no two
// turns overlap on one endpoint.
func (p *Participation) BeginTurn() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != Accepting {
		return ErrNotAccepting
	}
	p.state = Busy
	return nil
}

// EndTurn moves Busy -> Accepting. It fails from Disabled or Accepting.
func (p *Participation) EndTurn() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != Busy {
		return ErrNotBusy
	}
	p.state = Accepting
	return nil
}
