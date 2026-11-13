// Package roster tracks the guests connected to one host and the last guest
// who spoke substantively.
package roster

import (
	"fmt"
	"strings"
	"sync"
)

type ErrorKind string

const (
	ErrNoLastSpeaker ErrorKind = "no_last_speaker"
	ErrUnknownGuest  ErrorKind = "unknown_guest"
)

type RouteError struct {
	Kind   ErrorKind
	Nick   string
	Guests []string
	Last   string
}

func (e *RouteError) Error() string {
	guests := strings.Join(e.Guests, ", ")
	if guests == "" {
		guests = "none"
	}
	switch e.Kind {
	case ErrNoLastSpeaker:
		return fmt.Sprintf("not sent: no guest has spoken yet; connected guests: %s (use jkp say --to <nick> -- <text>)", guests)
	case ErrUnknownGuest:
		return fmt.Sprintf("not sent: unknown guest %s; connected guests: %s", e.Nick, guests)
	default:
		return fmt.Sprintf("not sent: cannot route to %s; connected guests: %s", e.Nick, guests)
	}
}

type Snapshot struct {
	Guests []string `json:"guests"`
	Last   string   `json:"last"`
}

type Roster struct {
	mu     sync.Mutex
	guests []string
	known  map[string]struct{}
	last   string
}

func New() *Roster {
	return &Roster{known: make(map[string]struct{})}
}

func (r *Roster) Add(nick string) {
	if nick == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(nick)
}

// Spoke records substantive activity. It also re-learns a guest after a host
// process restart, when the in-memory roster is empty.
func (r *Roster) Spoke(nick string) {
	if nick == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(nick)
	r.last = nick
}

func (r *Roster) Resolve(to string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nick := to
	if nick == "" {
		nick = r.last
		if nick == "" {
			return "", &RouteError{Kind: ErrNoLastSpeaker, Guests: append([]string(nil), r.guests...), Last: r.last}
		}
	}
	if _, ok := r.known[nick]; !ok {
		return "", &RouteError{Kind: ErrUnknownGuest, Nick: nick, Guests: append([]string(nil), r.guests...), Last: r.last}
	}
	r.last = nick
	return nick, nil
}

func (r *Roster) Remove(nick string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.known[nick]; !ok {
		return
	}
	delete(r.known, nick)
	for i, guest := range r.guests {
		if guest == nick {
			r.guests = append(r.guests[:i], r.guests[i+1:]...)
			break
		}
	}
	if r.last == nick {
		r.last = ""
	}
}

func (r *Roster) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Snapshot{Guests: append([]string(nil), r.guests...), Last: r.last}
}

func (r *Roster) addLocked(nick string) {
	if _, ok := r.known[nick]; ok {
		return
	}
	r.known[nick] = struct{}{}
	r.guests = append(r.guests, nick)
}
