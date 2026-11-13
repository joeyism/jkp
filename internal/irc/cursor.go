package irc

import (
	"sync"

	"github.com/joey/jkp/internal/protocol"
)

// Cursor tracks which inbound events an endpoint has already applied so a
// reconnect does not replay IRC history as fresh instructions. It is scoped
// per endpoint and uses two signals:
//
//  1. Exact event ID match (strong; used when the transport carries IRCv3 msgids).
//  2. Timestamp fallback: an event whose timestamp is not strictly after the
//     newest applied one is treated as history (used when IDs are absent).
//
// A timestamp of 0 disables the fallback and relies on ID matching alone.
type Cursor struct {
	mu    sync.Mutex
	ids   map[protocol.EndpointID]map[string]struct{}
	lastA map[protocol.EndpointID]int64
}

// NewCursor returns an empty replay cursor.
func NewCursor() *Cursor {
	return &Cursor{
		ids:   make(map[protocol.EndpointID]map[string]struct{}),
		lastA: make(map[protocol.EndpointID]int64),
	}
}

// Seen reports whether an event has already been applied to an endpoint.
func (c *Cursor) Seen(id protocol.EndpointID, eventID string, sentAt int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if known, ok := c.ids[id]; ok {
		if _, dup := known[eventID]; dup {
			return true
		}
	}
	if sentAt > 0 {
		if last, ok := c.lastA[id]; ok && sentAt <= last {
			return true
		}
	}
	return false
}

// Mark records an event as applied and advances the endpoint's watermark.
func (c *Cursor) Mark(id protocol.EndpointID, eventID string, sentAt int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	known, ok := c.ids[id]
	if !ok {
		known = make(map[string]struct{})
		c.ids[id] = known
	}
	known[eventID] = struct{}{}
	if sentAt > c.lastA[id] {
		c.lastA[id] = sentAt
	}
}

// Watermark returns the newest applied timestamp for an endpoint.
func (c *Cursor) Watermark(id protocol.EndpointID) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastA[id]
}
