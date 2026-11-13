package core

import (
	"github.com/joey/jkp/internal/protocol"
)

// ShouldProcess is the state-independent participation policy: is this the
// kind of event that may ever reach this endpoint?
//
//   - Direct messages always qualify.
//   - Room traffic qualifies only when it explicitly mentions the endpoint's
//     exact nick (never substring matches).
//   - An endpoint never triggers itself.
//
// participant input is never owner authorization regardless of this gate.
func ShouldProcess(ev protocol.InboundEvent, nick string) bool {
	if nick != "" && (ev.Sender.Nick == nick || ev.Sender.Account == nick) {
		return false
	}
	switch ev.Kind {
	case protocol.KindDM:
		return true
	case protocol.KindRoom:
		return mentions(ev.Mentions, nick)
	default:
		return false
	}
}

// ShouldAdmit reports whether an event may start a turn right now. It combines
// the participation policy with readiness: only an Accepting endpoint begins a
// turn immediately. A Busy endpoint still qualifies for processing but must
// queue instead; a Disabled endpoint admits nothing.
func ShouldAdmit(p *Participation, ev protocol.InboundEvent, nick string) bool {
	return p.State() == Accepting && ShouldProcess(ev, nick)
}

// mentions reports whether nick is named exactly. Order-insensitive; no
// substring or case-fold matching.
func mentions(list []string, nick string) bool {
	if nick == "" {
		return false
	}
	for _, m := range list {
		if m == nick {
			return true
		}
	}
	return false
}
