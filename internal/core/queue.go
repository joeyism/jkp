package core

import (
	"errors"
	"sync"

	"github.com/joey/jkp/internal/protocol"
)

// Queue errors.
var (
	ErrDuplicate = errors.New("duplicate event id")
	ErrQueueFull = errors.New("inbox full")
)

// Inbox is a per-endpoint bounded FIFO of admitted inbound events. It owns two
// guarantees and nothing else:
//
//  1. At-most-once admission per event ID within one participation generation.
//  2. A bounded depth so a runaway sender cannot exhaust memory.
//
// Disable drops queued work and resets dedupe state, marking a generation
// boundary. An event that was never dequeued may therefore be retried with the
// same ID after a Disable/Enable cycle; an event already dequeued (consumed by
// a turn) is fenced by the router's generation check instead, not here.
type Inbox struct {
	mu       sync.Mutex
	maxDepth int
	pending  []protocol.InboundEvent
	seen     map[string]struct{}
	gen      uint64
}

// NewInbox returns an empty inbox with the given maximum queue depth.
func NewInbox(maxDepth int) *Inbox {
	if maxDepth <= 0 {
		maxDepth = 8
	}
	return &Inbox{maxDepth: maxDepth, seen: make(map[string]struct{})}
}

// Enqueue adds an event. It returns ErrDuplicate when the ID is already known
// in this generation, and ErrQueueFull when the inbox is at capacity.
func (i *Inbox) Enqueue(ev protocol.InboundEvent) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, dup := i.seen[ev.ID]; dup {
		return ErrDuplicate
	}
	if len(i.pending) >= i.maxDepth {
		return ErrQueueFull
	}
	i.seen[ev.ID] = struct{}{}
	i.pending = append(i.pending, ev)
	return nil
}

// Dequeue removes and returns the oldest event.
func (i *Inbox) Dequeue() (protocol.InboundEvent, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.pending) == 0 {
		return protocol.InboundEvent{}, false
	}
	ev := i.pending[0]
	i.pending = i.pending[1:]
	return ev, true
}

// Len reports the number of queued events.
func (i *Inbox) Len() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.pending)
}

// Disable drops all queued work and resets dedupe state. It advances the
// generation fence. This is a hard stop: nothing queued under the previous
// generation is recoverable, and no late reply may refer to it.
func (i *Inbox) Disable() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.pending = nil
	i.seen = make(map[string]struct{})
	i.gen++
}

// Enable prepares the inbox to accept work again after a Disable. It does not
// resurrect dropped events.
func (i *Inbox) Enable() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return nil
}

// Generation reports the current fencing generation.
func (i *Inbox) Generation() uint64 {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.gen
}
