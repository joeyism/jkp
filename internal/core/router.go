package core

import (
	"errors"
	"sync"

	"github.com/joey/jkp/internal/protocol"
)

// Routing errors. These are authorization failures, not transport failures.
var (
	ErrUnknownEndpoint   = errors.New("unknown endpoint")
	ErrDuplicateEndpoint = errors.New("endpoint already registered")
	ErrInvalidEndpoint   = errors.New("endpoint requires an id and a nick")
	ErrUnknownEvent      = errors.New("unknown inbound event")
	ErrWrongEndpoint     = errors.New("event belongs to another endpoint")
	ErrTargetMismatch    = errors.New("reply target does not match inbound event")
	ErrStaleGeneration   = errors.New("reply refers to work from a disabled generation")
	ErrNotProcessable    = errors.New("event is not processable by this endpoint")
)

// Sender delivers one already-authorized outbound message. The core never
// sends raw assistant text; only explicit Reply output reaches the wire.
type Sender interface {
	Send(protocol.OutboundMessage) error
}

// endpointState bundles everything scoped to one agent endpoint.
type endpointState struct {
	part     *Participation
	in       *Inbox
	events   map[string]protocol.InboundEvent
	eventGen map[string]uint64
	nick     string
}

// Router enforces endpoint ownership, conversation isolation, and explicit
// reply targeting. It is the only path from a model's reply to the wire.
type Router struct {
	mu  sync.Mutex
	snd Sender
	eps map[protocol.EndpointID]*endpointState
	// owner indexes every admitted event ID to its endpoint so a reply can
	// distinguish "unknown event" from "someone else's event".
	owner map[string]protocol.EndpointID
}

// NewRouter returns a router that sends through snd.
func NewRouter(snd Sender) *Router {
	return &Router{
		snd:   snd,
		eps:   make(map[protocol.EndpointID]*endpointState),
		owner: make(map[string]protocol.EndpointID),
	}
}

// Register declares an endpoint and its exact IRC nick. Duplicate IDs fail.
func (r *Router) Register(id protocol.EndpointID, nick string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.eps[id]; exists {
		return ErrDuplicateEndpoint
	}
	r.eps[id] = &endpointState{
		part:     NewParticipation(),
		in:       NewInbox(8),
		events:   make(map[string]protocol.InboundEvent),
		eventGen: make(map[string]uint64),
		nick:     nick,
	}
	return nil
}

func (r *Router) state(id protocol.EndpointID) (*endpointState, error) {
	st, ok := r.eps[id]
	if !ok {
		return nil, ErrUnknownEndpoint
	}
	return st, nil
}

// Enable starts accepting work for an endpoint.
func (r *Router) Enable(id protocol.EndpointID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.state(id)
	if err != nil {
		return err
	}
	if err := st.part.Enable(); err != nil {
		return err
	}
	return st.in.Enable()
}

// Disable hard-stops an endpoint: queued work is dropped, the generation fence
// advances, and no reply may be sent for prior events. Admitted event records
// are retained so a late reply is rejected as ErrStaleGeneration rather than
// silently disappearing as ErrUnknownEvent; their generation no longer matches.
func (r *Router) Disable(id protocol.EndpointID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.state(id)
	if err != nil {
		return err
	}
	st.in.Disable()
	if err := st.part.Disable(); err != nil {
		return err
	}
	return nil
}

// Admit validates and records an inbound event. An Accepting endpoint starts a
// turn immediately; a Busy endpoint queues instead of starting a second turn;
// a Disabled endpoint rejects outright.
func (r *Router) Admit(ev protocol.InboundEvent) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.state(ev.EndpointID)
	if err != nil {
		return err
	}
	if !ShouldProcess(ev, st.nick) {
		return ErrNotProcessable
	}
	state := st.part.State()
	if state == Disabled {
		return ErrNotAccepting
	}
	if err := st.in.Enqueue(ev); err != nil {
		return err
	}
	gen := st.part.Generation()
	st.events[ev.ID] = ev
	st.eventGen[ev.ID] = gen
	r.owner[ev.ID] = ev.EndpointID
	// Only an accepting endpoint begins a turn; a busy one leaves the event
	// queued for the current turn's EndTurn to drain.
	if state == Accepting {
		if err := st.part.BeginTurn(); err != nil {
			return err
		}
	}
	return nil
}

// Event returns a previously admitted inbound event so callers can derive a
// reply's conversation coordinate. It never crosses endpoint boundaries.
func (r *Router) Event(id protocol.EndpointID, eventID string) (protocol.InboundEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.eps[id]
	if !ok {
		return protocol.InboundEvent{}, false
	}
	ev, ok := st.events[eventID]
	return ev, ok
}

// Next dequeues the oldest admitted event for an endpoint. It never crosses
// endpoint ownership boundaries.
func (r *Router) Next(id protocol.EndpointID) (protocol.InboundEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.eps[id]
	if !ok {
		return protocol.InboundEvent{}, false
	}
	return st.in.Dequeue()
}

// Reply authorizes and sends one explicit reply. The reply's kind and target
// must exactly match the conversation coordinate of the inbound event it
// names; nothing else can be emitted on its behalf.
func (r *Router) Reply(msg protocol.OutboundMessage) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	// Ownership is checked against the global index first so a cross-endpoint
	// reply is reported as such rather than as an unknown event.
	if owner, seen := r.owner[msg.InReplyTo]; seen {
		if owner != msg.EndpointID {
			r.mu.Unlock()
			return ErrWrongEndpoint
		}
	} else {
		r.mu.Unlock()
		return ErrUnknownEvent
	}
	st, err := r.state(msg.EndpointID)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	orig, known := st.events[msg.InReplyTo]
	if !known {
		r.mu.Unlock()
		return ErrUnknownEvent
	}
	if msg.Kind != orig.Kind || msg.Target != orig.ReplyTarget() {
		r.mu.Unlock()
		return ErrTargetMismatch
	}
	if st.eventGen[msg.InReplyTo] != st.part.Generation() || st.part.State() == Disabled {
		r.mu.Unlock()
		return ErrStaleGeneration
	}
	r.mu.Unlock()
	// Send outside the lock; Sender may block on IRC I/O.
	return r.snd.Send(msg)
}
