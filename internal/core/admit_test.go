package core_test

import (
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

func acceptParticipation(t *testing.T) *core.Participation {
	t.Helper()
	p := core.NewParticipation()
	if err := p.Enable(); err != nil {
		t.Fatal(err)
	}
	return p
}

func dmEvent(id, endpoint, sender, body string) protocol.InboundEvent {
	return protocol.InboundEvent{
		ID: id, EndpointID: protocol.EndpointID(endpoint), Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: sender}, Body: body,
	}
}

func TestAdmitDMWhenAccepting(t *testing.T) {
	t.Parallel()
	p := acceptParticipation(t)
	if !core.ShouldAdmit(p, dmEvent("1", "agent-a", "alice", "hi"), "agent-a") {
		t.Fatal("DM should be admitted")
	}
}

func TestRejectDMWhenDisabled(t *testing.T) {
	t.Parallel()
	p := core.NewParticipation()
	if core.ShouldAdmit(p, dmEvent("1", "agent-a", "alice", "hi"), "agent-a") {
		t.Fatal("disabled endpoint must reject")
	}
}

func TestRejectDMWhenBusy(t *testing.T) {
	t.Parallel()
	p := acceptParticipation(t)
	if err := p.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if core.ShouldAdmit(p, dmEvent("1", "agent-a", "alice", "hi"), "agent-a") {
		t.Fatal("busy endpoint must queue, not admit immediately")
	}
}

func TestAdmitRoomOnlyOnExplicitMention(t *testing.T) {
	t.Parallel()
	p := acceptParticipation(t)
	mentioned := protocol.InboundEvent{
		ID: "1", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Sender: protocol.Identity{Account: "alice"},
		Body: "hey agent-a look", Mentions: []string{"agent-a"},
	}
	unaddressed := mentioned
	unaddressed.ID = "2"
	unaddressed.Mentions = nil
	unaddressed.Body = "general chatter"
	if !core.ShouldAdmit(p, mentioned, "agent-a") {
		t.Fatal("mention should admit")
	}
	if core.ShouldAdmit(p, unaddressed, "agent-a") {
		t.Fatal("unaddressed room traffic must not admit")
	}
}

func TestMentionIsExactNotSubstring(t *testing.T) {
	t.Parallel()
	p := acceptParticipation(t)
	// Body contains agent-alpha which is not a mention of agent-a.
	lookalike := protocol.InboundEvent{
		ID: "1", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Sender: protocol.Identity{Account: "alice"},
		Body: "agent-alpha please check", Mentions: []string{"agent-alpha"},
	}
	if core.ShouldAdmit(p, lookalike, "agent-a") {
		t.Fatal("lookalike nick must not be treated as a mention")
	}
}

func TestSelfMentionDoesNotAdmit(t *testing.T) {
	t.Parallel()
	p := acceptParticipation(t)
	self := protocol.InboundEvent{
		ID: "1", EndpointID: "agent-a", Kind: protocol.KindRoom,
		Target: "#lab", Sender: protocol.Identity{Account: "agent-a"},
		Body: "agent-a notes", Mentions: []string{"agent-a"},
	}
	if core.ShouldAdmit(p, self, "agent-a") {
		t.Fatal("an endpoint must not be triggered by its own message")
	}
}
