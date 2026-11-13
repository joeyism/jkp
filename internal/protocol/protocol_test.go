package protocol_test

import (
	"strings"
	"testing"

	"github.com/joey/jkp/internal/protocol"
)

func TestInboundEventRejectsEmptyID(t *testing.T) {
	t.Parallel()
	ev := protocol.InboundEvent{
		EndpointID: "oc-author",
		Kind:       protocol.KindDM,
		Target:     "alice",
		Sender:     protocol.Identity{Account: "alice"},
		Body:       "hello",
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for empty event ID")
	}
}

func TestInboundEventRejectsEmptyBody(t *testing.T) {
	t.Parallel()
	ev := protocol.InboundEvent{
		ID:         "evt-1",
		EndpointID: "oc-author",
		Kind:       protocol.KindDM,
		Target:     "alice",
		Sender:     protocol.Identity{Account: "alice"},
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestInboundEventRejectsOversizedBody(t *testing.T) {
	t.Parallel()
	ev := protocol.InboundEvent{
		ID:         "evt-1",
		EndpointID: "oc-author",
		Kind:       protocol.KindDM,
		Target:     "alice",
		Sender:     protocol.Identity{Account: "alice"},
		Body:       strings.Repeat("a", protocol.MaxBodyBytes+1),
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for oversized body")
	}
}

func TestInboundEventRequiresKind(t *testing.T) {
	t.Parallel()
	ev := protocol.InboundEvent{
		ID:         "evt-1",
		EndpointID: "oc-author",
		Target:     "alice",
		Sender:     protocol.Identity{Account: "alice"},
		Body:       "hello",
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for missing kind")
	}
}

func TestInboundEventRequiresTarget(t *testing.T) {
	t.Parallel()
	ev := protocol.InboundEvent{
		ID:         "evt-1",
		EndpointID: "oc-author",
		Kind:       protocol.KindDM,
		Sender:     protocol.Identity{Account: "alice"},
		Body:       "hello",
	}
	if err := ev.Validate(); err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestValidInboundEventPasses(t *testing.T) {
	t.Parallel()
	ev := protocol.InboundEvent{
		ID:         "evt-1",
		EndpointID: "oc-author",
		Kind:       protocol.KindDM,
		Target:     "alice",
		Sender:     protocol.Identity{Account: "alice"},
		Body:       "hello",
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOutboundReplyRequiresTarget(t *testing.T) {
	t.Parallel()
	msg := protocol.OutboundMessage{
		ID:         "out-1",
		EndpointID: "oc-author",
		Body:       "pushback",
		InReplyTo:  "evt-1",
	}
	if err := msg.Validate(); err == nil {
		t.Fatal("expected error for missing kind/target")
	}
}

func TestOutboundReplyRequiresInReplyTo(t *testing.T) {
	t.Parallel()
	msg := protocol.OutboundMessage{
		ID:         "out-1",
		EndpointID: "oc-author",
		Kind:       protocol.KindDM,
		Target:     "alice",
		Body:       "pushback",
	}
	if err := msg.Validate(); err == nil {
		t.Fatal("expected error for missing in_reply_to")
	}
}

func TestNicknameIsNotOwnerProof(t *testing.T) {
	t.Parallel()
	id := protocol.Identity{Nick: "joey", Account: ""}
	if id.Authenticated() {
		t.Fatal("nick without account must not be authenticated")
	}
}

func TestAccountCountAsAuthenticated(t *testing.T) {
	t.Parallel()
	id := protocol.Identity{Nick: "joey", Account: "joey"}
	if !id.Authenticated() {
		t.Fatal("account should authenticate identity")
	}
}
