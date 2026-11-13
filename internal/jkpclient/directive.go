package jkpclient

import (
	"regexp"
	"strings"
)

// DirectiveMarker opens an agent-to-peer send block. It is a protocol literal:
// the model must write it exactly for it to be honored.
const DirectiveMarker = "jkp-send"

// directiveRe matches one send block:
//
//	[jkp-send to=PEER]body[/jkp-send]
//
// The target may be bare or quoted. The body may span lines. Matching is
// case-sensitive on purpose: a model that writes the marker differently is not
// silently reinterpreted as a command.
var directiveRe = regexp.MustCompile(
	`(?s)\[jkp-send\s+to=["']?([^\]"'\s]+)["']?\s*\](.*?)\[/jkp-send\]`)

// Directive is one outbound message the agent asked to send to a peer.
type Directive struct {
	To   string
	Body string
}

// ParsedReply separates an agent's reply into peer-directed sends and the text
// that should answer the original sender. Plain text yields no sends and is
// returned verbatim.
type ParsedReply struct {
	Sends     []Directive
	Reply     string
	LocalOnly bool
}

// ParseReply extracts send directives and the remaining reply text. An
// unclosed directive is not executed; its text survives in Reply so nothing is
// silently dropped.
func ParseReply(text string) ParsedReply {
	var out ParsedReply
	decision := Classify(text)
	if decision.Action == ActionLocalOnly {
		out.Reply = decision.Body
		out.LocalOnly = true
		return out
	}
	text = decision.Body
	for _, m := range directiveRe.FindAllStringSubmatch(text, -1) {
		to := strings.TrimSpace(m[1])
		body := strings.TrimSpace(m[2])
		if to == "" || body == "" {
			continue
		}
		out.Sends = append(out.Sends, Directive{To: to, Body: body})
	}

	remainder := directiveRe.ReplaceAllString(text, "")
	// Where blocks were removed, blank lines are left behind. Collapse runs of
	// whitespace-only lines so the reply reads naturally.
	lines := strings.Split(remainder, "\n")
	kept := make([]string, 0, len(lines))
	blank := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			if blank {
				continue
			}
			blank = true
			kept = append(kept, "")
			continue
		}
		blank = false
		kept = append(kept, ln)
	}
	out.Reply = strings.TrimSpace(strings.Join(kept, "\n"))
	return out
}
