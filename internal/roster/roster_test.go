package roster

import (
	"errors"
	"sync"
	"testing"
)

func TestAddListsInJoinOrder(t *testing.T) {
	r := New()
	r.Add("g1")
	r.Add("g2")
	r.Add("g1")
	if got := r.Snapshot().Guests; !equal(got, []string{"g1", "g2"}) {
		t.Fatalf("guests = %v", got)
	}
}

func TestSpokeSetsLastSpeakerAndAddsUnknown(t *testing.T) {
	r := New()
	r.Spoke("g1")
	if snap := r.Snapshot(); snap.Last != "g1" || !equal(snap.Guests, []string{"g1"}) {
		t.Fatalf("snapshot = %#v", snap)
	}
}

func TestResolveEmptyUsesLastSpeaker(t *testing.T) {
	r := New()
	r.Add("g1")
	r.Spoke("g1")
	got, err := r.Resolve("")
	if err != nil || got != "g1" {
		t.Fatalf("Resolve = %q, %v; want g1", got, err)
	}
}

func TestResolveEmptyWithoutLastSpeakerErrorsWithRoster(t *testing.T) {
	r := New()
	r.Add("g1")
	_, err := r.Resolve("")
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Kind != ErrNoLastSpeaker || !equal(routeErr.Guests, []string{"g1"}) {
		t.Fatalf("Resolve error = %#v, want no-last-speaker with roster", err)
	}
}

func TestResolveExplicitSetsLastSpeaker(t *testing.T) {
	r := New()
	r.Add("g1")
	r.Add("g2")
	got, err := r.Resolve("g2")
	if err != nil || got != "g2" || r.Snapshot().Last != "g2" {
		t.Fatalf("Resolve = %q, %v, snapshot=%#v", got, err, r.Snapshot())
	}
}

func TestResolveUnknownErrorsWithRoster(t *testing.T) {
	r := New()
	r.Add("g1")
	_, err := r.Resolve("g2")
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Kind != ErrUnknownGuest || routeErr.Nick != "g2" || !equal(routeErr.Guests, []string{"g1"}) {
		t.Fatalf("Resolve error = %#v, want unknown guest g2 and roster", err)
	}
}

func TestRemoveClearsLastSpeaker(t *testing.T) {
	r := New()
	r.Spoke("g1")
	r.Remove("g1")
	if snap := r.Snapshot(); snap.Last != "" || len(snap.Guests) != 0 {
		t.Fatalf("snapshot = %#v", snap)
	}
}

func TestRemoveOtherKeepsLastSpeaker(t *testing.T) {
	r := New()
	r.Spoke("g1")
	r.Add("g2")
	r.Remove("g2")
	if snap := r.Snapshot(); snap.Last != "g1" || !equal(snap.Guests, []string{"g1"}) {
		t.Fatalf("snapshot = %#v", snap)
	}
}

func TestSnapshotReturnsCopies(t *testing.T) {
	r := New()
	r.Add("g1")
	snap := r.Snapshot()
	snap.Guests[0] = "changed"
	if got := r.Snapshot().Guests[0]; got != "g1" {
		t.Fatalf("roster changed through snapshot: %q", got)
	}
}

func TestRosterIsRaceFree(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			nick := "g" + string(rune('a'+i))
			for j := 0; j < 200; j++ {
				r.Add(nick)
				r.Spoke(nick)
				_, _ = r.Resolve("")
				_ = r.Snapshot()
				if j%3 == 0 {
					r.Remove(nick)
				}
			}
		}(i)
	}
	wg.Wait()
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
