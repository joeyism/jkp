package irc_test

import (
	"testing"

	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

func ep(id string) protocol.EndpointID { return protocol.EndpointID(id) }

func TestCursorSeenAfterMark(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	c.Mark(ep("agent-a"), "evt-1", 100)
	if !c.Seen(ep("agent-a"), "evt-1", 100) {
		t.Fatal("marked event must be seen")
	}
}

func TestCursorUnknownIsNotSeen(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	if c.Seen(ep("agent-a"), "evt-2", 200) {
		t.Fatal("unknown event must not be seen")
	}
}

func TestCursorIsPerEndpoint(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	c.Mark(ep("agent-a"), "evt-1", 100)
	if c.Seen(ep("agent-b"), "evt-1", 100) {
		t.Fatal("cursor must be scoped per endpoint")
	}
}

func TestCursorTimestampFallbackIgnoresOlderReplay(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	c.Mark(ep("agent-a"), "evt-1", 100)
	// A replayed event with no matching ID but an older timestamp is history.
	if !c.Seen(ep("agent-a"), "evt-replay", 50) {
		t.Fatal("older-than-cursor replay must be ignored")
	}
	// A newer event is fresh work.
	if c.Seen(ep("agent-a"), "evt-fresh", 150) {
		t.Fatal("newer event must not be treated as history")
	}
}

func TestCursorBoundaryTimestampIsHistory(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	c.Mark(ep("agent-a"), "evt-1", 100)
	// Not strictly after the cursor: treat as history.
	if !c.Seen(ep("agent-a"), "evt-same", 100) {
		t.Fatal("equal timestamp must be treated as history")
	}
}

func TestCursorZeroTimestampReliesOnIDOnly(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	c.Mark(ep("agent-a"), "evt-1", 0)
	if !c.Seen(ep("agent-a"), "evt-1", 0) {
		t.Fatal("ID match must work without timestamps")
	}
	if c.Seen(ep("agent-a"), "evt-other", 0) {
		t.Fatal("different ID with no timestamp must not be seen")
	}
}

func TestCursorReplaySequence(t *testing.T) {
	t.Parallel()
	c := irc.NewCursor()
	// Processing evt-1 advances the cursor.
	c.Mark(ep("agent-a"), "evt-1", 100)
	// Reconnect replays evt-1 then delivers evt-2.
	if c.Seen(ep("agent-a"), "evt-1", 100) {
		// expected: skip
	} else {
		t.Fatal("replayed evt-1 must be skipped")
	}
	if c.Seen(ep("agent-a"), "evt-2", 200) {
		t.Fatal("evt-2 is new work and must be delivered")
	}
	c.Mark(ep("agent-a"), "evt-2", 200)
	if !c.Seen(ep("agent-a"), "evt-2", 200) {
		t.Fatal("evt-2 must be seen after processing")
	}
}
