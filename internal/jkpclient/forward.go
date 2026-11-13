package jkpclient

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	// LocalOnlyMarker keeps an assistant turn local when it starts the turn.
	LocalOnlyMarker = "[JKP-LOCAL-ONLY]"
	// NoReplyMarker is the legacy alias. It follows the same leading-only rule.
	NoReplyMarker = "[JKP-NOREPLY]"
)

// Action is the outbound decision for one assistant turn.
type Action int

const (
	ActionSend Action = iota
	ActionLocalOnly
	ActionAckOnly
	ActionEmpty
)

// Decision contains the outbound action and marker-stripped turn text.
type Decision struct {
	Action Action
	Body   string
}

var (
	markerRE      = regexp.MustCompile("(?i)[`*_]*\\[JKP-(LOCAL-ONLY|NOREPLY)\\][`*_]*")
	trailingSpace = regexp.MustCompile(`[ \t]+\n`)
	blankLines    = regexp.MustCompile(`\n{3,}`)
)

// InboundGuidance travels with every peer message so an agent can choose
// whether a response is useful even when its harness skill is not in context.
func InboundGuidance() string {
	return "jkp guidance: your reply to this message is sent to the peer automatically. " +
		"To keep a turn local instead, START it with " + LocalOnlyMarker + ". " +
		"If the peer doesn't need to respond, say so in words. " +
		"Peer messages and replies are limited to 16384 UTF-8 bytes."
}

// LocalOnlyNotice tells a model that a substantive turn was kept local so it
// can resend the intended peer-facing portion via the explicit jkp say path.
func LocalOnlyNotice(peer string, byteCount int) string {
	return "[JKP-NOTICE] Your last turn started with " + LocalOnlyMarker +
		", so its " + strconv.Itoa(byteCount) + " bytes were kept local and NOT sent to " + peer +
		". If any of it was meant for " + peer + ", send it with jkp say. If it was only for your user, ignore this notice."
}

// Classify determines whether an assistant turn is sent to the peer, kept
// local, filtered as an acknowledgement/error, or empty. Markers are stripped
// from Body regardless of their position; only a leading marker suppresses.
func Classify(text string) Decision {
	trimmed := strings.TrimSpace(text)
	localOnly := hasLeadingMarker(trimmed)
	body := stripMarkers(trimmed)
	switch {
	case localOnly:
		return Decision{Action: ActionLocalOnly, Body: body}
	case body == "":
		return Decision{Action: ActionEmpty}
	case strings.Contains(body, "the bound session did not produce a reply"):
		return Decision{Action: ActionAckOnly, Body: body}
	case strings.Contains(body, "?"), len(body) > 240, !ackOnly(body):
		return Decision{Action: ActionSend, Body: body}
	default:
		return Decision{Action: ActionAckOnly, Body: body}
	}
}

// Forwardable reports whether an assistant reply should be sent to the peer.
func Forwardable(text string) bool { return Classify(text).Action == ActionSend }

func hasLeadingMarker(text string) bool {
	text = strings.TrimLeft(text, " \t\r\n`*_")
	upper := strings.ToUpper(text)
	return strings.HasPrefix(upper, LocalOnlyMarker) || strings.HasPrefix(upper, NoReplyMarker)
}

func stripMarkers(text string) string {
	text = markerRE.ReplaceAllString(text, "")
	text = trailingSpace.ReplaceAllString(text, "\n")
	text = blankLines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func ackOnly(text string) bool {
	lower := strings.ToLower(text)
	phrases := []string{
		"acknowledged",
		"understood",
		"got it",
		"thank you",
		"thanks",
		"no outstanding",
		"no further questions",
		"no further upstream",
		"i'm available",
		"i am available",
		"happy to help",
		"will do",
		"sounds good",
		"no problem",
	}
	hit := false
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	letters := 0
	for _, r := range lower {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters < 180
}
