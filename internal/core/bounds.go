package core

import (
	"errors"
	"sync"
	"time"

	"github.com/joey/jkp/internal/protocol"
)

// Dialogue bounds. Autonomous agent-to-agent conversation must terminate, so
// every conversation is capped in both turns and wall-clock age, and only an
// explicit Finish marks it complete.
const (
	// MaxAutonomousTurns caps admitted turns in one conversation.
	MaxAutonomousTurns = 12
	// MaxAutonomousAge caps elapsed time since a conversation's first admission.
	MaxAutonomousAge = 15 * time.Minute
)

// Bounds errors.
var (
	ErrTooManyTurns         = errors.New("conversation turn limit reached")
	ErrConversationExpired  = errors.New("conversation exceeded max age")
	ErrConversationFinished = errors.New("conversation already finished")
)

// ConvoStatus is a conversation's terminal classification.
type ConvoStatus string

const (
	// StatusUnknown is a conversation never admitted.
	StatusUnknown ConvoStatus = "unknown"
	// StatusActive is an admitted conversation still accepting turns.
	StatusActive ConvoStatus = "active"
	// StatusFinished is one explicitly finished by the reviewer/agent.
	StatusFinished ConvoStatus = "finished"
	// StatusIncomplete ended by disconnect without Finish; it is never
	// silently promoted to Finished.
	StatusIncomplete ConvoStatus = "incomplete"
)

// Clock abstracts time so tests can drive expiry deterministically.
type Clock interface {
	Now() time.Time
}

type convoState struct {
	turns     int
	startedAt time.Time
	lastAt    time.Time
	status    ConvoStatus
}

// BoundsConfig configures turn and age limits for peer conversations. MaxAge
// equal to zero disables wall-clock expiry; IdleReset resets a volley after a
// quiet interval without ending the conversation.
type BoundsConfig struct {
	MaxTurns  int
	MaxAge    time.Duration
	IdleReset time.Duration
}

// Bounds tracks turn counts, age, and completion per conversation.
type Bounds struct {
	mu    sync.Mutex
	clock Clock
	conv  map[protocol.Conversation]*convoState
	cfg   BoundsConfig
}

// NewBounds returns bounds tracking using the given clock.
func NewBounds(clock Clock) *Bounds {
	return NewBoundsWithConfig(clock, BoundsConfig{MaxTurns: MaxAutonomousTurns, MaxAge: MaxAutonomousAge})
}

// NewBoundsWithConfig returns bounds using caller-selected limits.
func NewBoundsWithConfig(clock Clock, cfg BoundsConfig) *Bounds {
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = MaxAutonomousTurns
	}
	return &Bounds{clock: clock, conv: make(map[protocol.Conversation]*convoState), cfg: cfg}
}

// Admit records one turn for a conversation. It rejects finished conversations,
// ones past MaxAutonomousAge (strictly greater than the limit), and ones at the
// turn cap. The first admission starts the conversation's clock.
func (b *Bounds) Admit(c protocol.Conversation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.conv[c]
	now := b.clock.Now()
	if !ok {
		b.conv[c] = &convoState{turns: 1, startedAt: now, lastAt: now, status: StatusActive}
		return nil
	}
	switch st.status {
	case StatusFinished:
		return ErrConversationFinished
	case StatusIncomplete:
		return ErrConversationFinished
	}
	if b.cfg.MaxAge > 0 && now.Sub(st.startedAt) > b.cfg.MaxAge {
		st.status = StatusIncomplete
		return ErrConversationExpired
	}
	if b.cfg.IdleReset > 0 && !st.lastAt.IsZero() && now.Sub(st.lastAt) > b.cfg.IdleReset {
		st.turns = 0
	}
	if st.turns >= b.cfg.MaxTurns {
		return ErrTooManyTurns
	}
	st.turns++
	st.lastAt = now
	return nil
}

// Reset starts a fresh bounded volley in an active conversation. A finished
// conversation remains closed.
func (b *Bounds) Reset(c protocol.Conversation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.conv[c]
	if !ok || st.status == StatusFinished {
		return
	}
	st.turns = 0
	st.startedAt = b.clock.Now()
	st.lastAt = st.startedAt
	st.status = StatusActive
}

// Finish marks a conversation complete. Repeated Finish is harmless. A
// conversation that never admitted a turn is still recorded as finished, so a
// reviewer who joins and leaves without findings is distinguishable from one
// that never participated at all.
func (b *Bounds) Finish(c protocol.Conversation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.conv[c]
	if !ok {
		now := b.clock.Now()
		b.conv[c] = &convoState{turns: 0, startedAt: now, lastAt: now, status: StatusFinished}
		return nil
	}
	st.status = StatusFinished
	return nil
}

// Disconnect records that a participant went away without finishing. A finished
// conversation keeps its finished status.
func (b *Bounds) Disconnect(c protocol.Conversation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.conv[c]
	if !ok {
		return
	}
	if st.status != StatusFinished {
		st.status = StatusIncomplete
	}
}

// Status reports a conversation's classification.
func (b *Bounds) Status(c protocol.Conversation) ConvoStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.conv[c]
	if !ok {
		return StatusUnknown
	}
	return st.status
}

// Turns reports how many turns a conversation has admitted.
func (b *Bounds) Turns(c protocol.Conversation) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st, ok := b.conv[c]; ok {
		return st.turns
	}
	return 0
}
