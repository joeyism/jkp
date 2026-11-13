package irc

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSplitBodyKeepsShortMessagesUnframed(t *testing.T) {
	parts := splitBody("hello")
	if len(parts) != 1 || parts[0] != "hello" {
		t.Fatalf("parts = %#v", parts)
	}
}

func TestSplitBodyChunksLongMessagesUnderIRCLimit(t *testing.T) {
	body := strings.Repeat("abcdefghij", 80) // 800 bytes
	parts := splitBody(body)
	if len(parts) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(parts))
	}
	var got strings.Builder
	for i, part := range parts {
		if len(part) > maxIRCBody {
			t.Fatalf("chunk %d length %d exceeds %d", i, len(part), maxIRCBody)
		}
		index, total, payload, ok := parseChunk(part)
		if !ok || total != len(parts) || index != i+1 {
			t.Fatalf("chunk %d parse = %d/%d ok=%v", i, index, total, ok)
		}
		got.WriteString(payload)
	}
	if got.String() != body {
		t.Fatal("reassembled body does not match")
	}
}

func TestChunkAssemblerJoinsInOrder(t *testing.T) {
	var a chunkAssembler
	if _, done := a.add("id", 1, 2, "hel", time.Now()); done {
		t.Fatal("first chunk should be incomplete")
	}
	got, done := a.add("id", 2, 2, "lo", time.Now())
	if !done || got != "hello" {
		t.Fatalf("got %q done=%v", got, done)
	}
}

func TestSplitBodyKeepsEveryUTF8RuneInOneChunk(t *testing.T) {
	body := strings.Repeat("facing left” — dimensions 2000×2000 🚀 ", 240)
	parts := splitBody(body)
	if len(parts) < 20 {
		t.Fatalf("expected many chunks, got %d", len(parts))
	}
	var got strings.Builder
	var messageID string
	for i, part := range parts {
		if len(part) > maxIRCBody {
			t.Fatalf("chunk %d length %d exceeds %d", i+1, len(part), maxIRCBody)
		}
		if !utf8.ValidString(part) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i+1, part)
		}
		id, index, total, payload, ok := parseChunkWithID(part)
		if !ok || index != i+1 || total != len(parts) {
			t.Fatalf("chunk %d parsed as id=%q %d/%d ok=%v", i+1, id, index, total, ok)
		}
		if messageID == "" {
			messageID = id
		}
		if id != messageID {
			t.Fatalf("chunk %d ID %q differs from %q", i+1, id, messageID)
		}
		got.WriteString(payload)
	}
	if got.String() != body {
		t.Fatalf("reassembled body differs: got %d bytes, want %d", got.Len(), len(body))
	}
}

func TestChunkAssemblerDoesNotSpliceDistinctMessagesWithSameTotal(t *testing.T) {
	now := time.Now()
	var a chunkAssembler
	if _, done := a.add("old", 1, 2, "old first ", now); done {
		t.Fatal("first chunk of old message unexpectedly completed")
	}
	if _, done := a.add("new", 1, 2, "new first ", now.Add(time.Second)); done {
		t.Fatal("first chunk of new message unexpectedly completed")
	}
	got, done := a.add("new", 2, 2, "new second", now.Add(2*time.Second))
	if !done || got != "new first new second" {
		t.Fatalf("got %q done=%v; old chunk must not be spliced", got, done)
	}
}

func TestChunkAssemblerExpiresOldPartialMessage(t *testing.T) {
	now := time.Now()
	var a chunkAssembler
	if _, done := a.add("same", 1, 2, "stale ", now); done {
		t.Fatal("first chunk unexpectedly completed")
	}
	if _, done := a.add("same", 2, 2, "late", now.Add(chunkTTL+time.Second)); done {
		t.Fatal("expired chunk assembly must not complete")
	}
	if a.have != 1 {
		t.Fatalf("expired assembly should restart with one chunk, have=%d", a.have)
	}
}
