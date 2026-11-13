package store_test

import (
	"path/filepath"
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
	"github.com/joey/jkp/internal/store"
)

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jkp.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ep(id, harness, session, nick string) core.Endpoint {
	return core.Endpoint{ID: protocol.EndpointID(id), Harness: harness, SessionID: session, Nick: nick}
}

func TestEndpointRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	want := ep("agent-a", "opencode", "ses_a", "agent-a")
	if err := s.SaveEndpoint(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("want %+v, got %+v", want, got)
	}
}

func TestEmptyDBReturnsEmptySlices(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	eps, err := s.LoadEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Fatalf("want no endpoints, got %d", len(eps))
	}
	c, err := s.LoadCursor("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.IDs) != 0 || c.Watermark != 0 {
		t.Fatalf("want empty cursor, got %+v", c)
	}
	g, err := s.LoadGeneration("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if g != 0 {
		t.Fatalf("want generation 0, got %d", g)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	if err := s.SaveCursor("agent-a", "evt-1", 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCursor("agent-a", "evt-2", 200); err != nil {
		t.Fatal(err)
	}
	c, err := s.LoadCursor("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.IDs) != 2 {
		t.Fatalf("want 2 IDs, got %+v", c.IDs)
	}
	if c.Watermark != 200 {
		t.Fatalf("want watermark 200, got %d", c.Watermark)
	}
}

func TestCursorIsPerEndpoint(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	if err := s.SaveCursor("agent-a", "evt-1", 100); err != nil {
		t.Fatal(err)
	}
	c, err := s.LoadCursor("agent-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.IDs) != 0 {
		t.Fatalf("cursor must be scoped per endpoint, got %+v", c)
	}
}

func TestGenerationSurvivesReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jkp.db")

	s1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SaveGeneration("agent-a", 3); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	g, err := s2.LoadGeneration("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if g != 3 {
		t.Fatalf("want generation 3 after reopen, got %d", g)
	}
}

func TestBumpGenerationIncrements(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	first, err := s.BumpGeneration("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BumpGeneration("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if second != first+1 {
		t.Fatalf("want %d, got %d", first+1, second)
	}
	g, err := s.LoadGeneration("agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if g != second {
		t.Fatalf("persisted generation %d != bumped %d", g, second)
	}
}

func TestSaveEndpointUpserts(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	if err := s.SaveEndpoint(ep("agent-a", "pi", "ses_a", "agent-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEndpoint(ep("agent-a", "opencode", "ses_a2", "agent-a")); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Harness != "opencode" {
		t.Fatalf("want upserted endpoint, got %+v", got)
	}
}
