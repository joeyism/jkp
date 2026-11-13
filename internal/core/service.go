package core

import (
	"sync"

	"github.com/joey/jkp/internal/protocol"
)

// Endpoint is the registration record for one agent endpoint: a harness
// session made addressable on IRC. SessionID is the exact harness session to
// bind; it is never re-resolved from "whichever session is active".
type Endpoint struct {
	ID        protocol.EndpointID `json:"id"`
	Harness   string              `json:"harness"`
	SessionID string              `json:"session_id"`
	Nick      string              `json:"nick"`
}

// Service composes the router, dialogue bounds, and endpoint registry into the
// surface used by the IRC bridge and the local adapter API. It owns no
// transport and no model invocation.
type Service struct {
	router *Router
	bounds *Bounds
	clock  Clock

	mu    sync.Mutex
	eps   map[protocol.EndpointID]Endpoint
	order []protocol.EndpointID
}

// NewService returns a service sending through snd and timing via clock.
func NewService(snd Sender, clock Clock) *Service {
	return NewServiceWithBounds(snd, clock, BoundsConfig{MaxTurns: MaxAutonomousTurns, MaxAge: MaxAutonomousAge})
}

// NewServiceWithBounds returns a service with explicit peer-dialogue bounds.
func NewServiceWithBounds(snd Sender, clock Clock, cfg BoundsConfig) *Service {
	return &Service{
		router: NewRouter(snd),
		bounds: NewBoundsWithConfig(clock, cfg),
		clock:  clock,
		eps:    make(map[protocol.EndpointID]Endpoint),
	}
}

// ResetConversation starts a fresh autonomous volley in one peer conversation.
func (s *Service) ResetConversation(conv protocol.Conversation) {
	s.bounds.Reset(conv)
}

// Register declares an endpoint and binds its exact harness session.
func (s *Service) Register(ep Endpoint) error {
	if ep.ID == "" || ep.Nick == "" {
		return ErrInvalidEndpoint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.eps[ep.ID]; exists {
		return ErrDuplicateEndpoint
	}
	if err := s.router.Register(ep.ID, ep.Nick); err != nil {
		return err
	}
	s.eps[ep.ID] = ep
	s.order = append(s.order, ep.ID)
	return nil
}

// List returns registered endpoints in registration order.
func (s *Service) List() []Endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Endpoint, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.eps[id])
	}
	return out
}

// Get returns one registered endpoint.
func (s *Service) Get(id protocol.EndpointID) (Endpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.eps[id]
	return ep, ok
}

// Enable starts accepting work for an endpoint.
func (s *Service) Enable(id protocol.EndpointID) error {
	return s.router.Enable(id)
}

// Disable hard-stops an endpoint. Conversation history and completion state
// are retained so a final batch can still be assembled afterward.
func (s *Service) Disable(id protocol.EndpointID) error {
	return s.router.Disable(id)
}

// Admit validates an inbound event, checks dialogue bounds for the peer
// conversation it belongs to, then routes it. A busy endpoint queues rather
// than starting a second turn; a disabled endpoint rejects outright.
func (s *Service) Admit(ev protocol.InboundEvent) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	conversation := ConversationFor(ev)
	if ev.HumanInitiated {
		s.bounds.Reset(conversation)
	}
	if err := s.bounds.Admit(conversation); err != nil {
		return err
	}
	return s.router.Admit(ev)
}

// Event returns a previously admitted inbound event for an endpoint.
func (s *Service) Event(id protocol.EndpointID, eventID string) (protocol.InboundEvent, bool) {
	return s.router.Event(id, eventID)
}

// Next dequeues the oldest pending event for an endpoint.
func (s *Service) Next(id protocol.EndpointID) (protocol.InboundEvent, bool) {
	return s.router.Next(id)
}

// Reply authorizes and sends one explicit reply.
func (s *Service) Reply(msg protocol.OutboundMessage) error {
	return s.router.Reply(msg)
}

// Finish marks one peer conversation complete. It is idempotent and harmless
// for a conversation that never sent anything.
func (s *Service) Finish(id protocol.EndpointID, conv protocol.Conversation) error {
	if _, ok := s.Get(id); !ok {
		return ErrUnknownEndpoint
	}
	return s.bounds.Finish(conv)
}

// Disconnect records that a peer went away without finishing. It never
// promotes the conversation to Finished.
func (s *Service) Disconnect(id protocol.EndpointID, conv protocol.Conversation) {
	s.bounds.Disconnect(conv)
}

// Status reports one conversation's classification.
func (s *Service) Status(conv protocol.Conversation) ConvoStatus {
	return s.bounds.Status(conv)
}

// ConversationFor derives the peer conversation an inbound event belongs to.
// Direct messages are one conversation per sender; room traffic is one
// conversation per channel. This is the same coordinate a reply must use.
func ConversationFor(ev protocol.InboundEvent) protocol.Conversation {
	return protocol.Conversation{Kind: ev.Kind, Target: ev.ReplyTarget()}
}
