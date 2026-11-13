package irc

import (
	"errors"
	"sync"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

// ErrReplayed is returned when a transport event has already been applied.
var ErrReplayed = errors.New("event already applied; ignoring replayed history")

// Bridge carries transport events into the collaboration service. It owns no
// admission policy: the service decides whether an event is admitted. It does
// own replay suppression so a reconnect cannot re-deliver history as fresh
// instructions.
type Bridge struct {
	svc    *core.Service
	cursor *Cursor

	mu    sync.Mutex
	stops map[protocol.EndpointID]func()
}

// NewBridge returns a bridge delivering into svc.
func NewBridge(svc *core.Service) *Bridge {
	return &Bridge{svc: svc, cursor: NewCursor(), stops: make(map[protocol.EndpointID]func())}
}

// Deliver moves one transport event into the service synchronously. Events
// already applied to the endpoint are rejected as ErrReplayed.
func (b *Bridge) Deliver(ev protocol.InboundEvent) error {
	if b.cursor.Seen(ev.EndpointID, ev.ID, ev.SentAt) {
		return ErrReplayed
	}
	if err := b.svc.Admit(ev); err != nil {
		return err
	}
	b.cursor.Mark(ev.EndpointID, ev.ID, ev.SentAt)
	return nil
}

// Attach binds a transport to an endpoint and pumps its events into the
// service until the transport closes or the returned stop function is called.
// Only one transport may be attached per endpoint at a time.
func (b *Bridge) Attach(id protocol.EndpointID, c Client) (stop func()) {
	return b.AttachWithErrors(id, c, nil)
}

// AttachWithErrors binds a transport and optionally reports events rejected
// by the service admission policy. ErrReplayed is intentionally omitted.
func (b *Bridge) AttachWithErrors(id protocol.EndpointID, c Client, onError func(error)) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(done)
			b.mu.Lock()
			delete(b.stops, id)
			b.mu.Unlock()
		})
	}

	b.mu.Lock()
	prev, exists := b.stops[id]
	b.stops[id] = stop
	b.mu.Unlock()
	// Stop the previous transport outside the lock: its stop function takes
	// the same mutex, and Go mutexes are not reentrant.
	if exists {
		prev()
	}

	go func() {
		for {
			select {
			case <-done:
				return
			case ev, open := <-c.Events():
				if !open {
					return
				}
				// The transport is authoritative for which endpoint it
				// serves; stamp it so policy sees the right owner.
				ev.EndpointID = id
				if err := b.Deliver(ev); err != nil && !errors.Is(err, ErrReplayed) && onError != nil {
					onError(err)
				}
			}
		}
	}()
	return stop
}
