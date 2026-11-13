package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/protocol"
)

func peerEvent() protocol.InboundEvent {
	return protocol.InboundEvent{
		ID: "evt-1", EndpointID: "guest", Kind: protocol.KindDM, Target: "guest",
		Sender: protocol.Identity{Nick: "host"}, Body: "two questions",
	}
}

func collect(logs *[]string) func(string, ...any) {
	return func(format string, args ...any) {
		*logs = append(*logs, fmt.Sprintf(format, args...))
	}
}

func containsLine(lines []string, part string) bool {
	for _, line := range lines {
		if strings.Contains(line, part) {
			return true
		}
	}
	return false
}

func TestHandleReplySendsSignoffMarkerTurnWithoutMarker(t *testing.T) {
	sender := &sizeCheckingSender{}
	h := &deliveryNoticeHarness{}
	reply := "Answers to your two questions. " + strings.Repeat("detail ", 600) + "[JKP-NOREPLY]"
	var logs []string
	handleReply(context.Background(), Config{EndpointID: "guest"}, sender, h, peerEvent(), reply, collect(&logs))
	msgs := sender.snapshot()
	if len(msgs) != 1 || strings.Contains(msgs[0].Body, "JKP-") {
		t.Fatalf("sent = %#v", msgs)
	}
	if len(h.notices) != 0 {
		t.Fatalf("no notice expected for a sent turn: %v", h.notices)
	}
}

func TestHandleReplyLeadingMarkerKeepsLocalAndNotifiesOpenCode(t *testing.T) {
	sender := &sizeCheckingSender{}
	h := &deliveryNoticeHarness{}
	reply := "[JKP-LOCAL-ONLY]\nPlan for my user: " + strings.Repeat("step ", 100)
	var logs []string
	handleReply(context.Background(), Config{EndpointID: "guest"}, sender, h, peerEvent(), reply, collect(&logs))
	if got := len(sender.snapshot()); got != 0 {
		t.Fatalf("local-only turn was sent (%d)", got)
	}
	if len(h.notices) != 1 {
		t.Fatalf("notices = %v", h.notices)
	}
	for _, want := range []string{"[JKP-NOTICE]", "kept local", "NOT sent to host", "jkp say"} {
		if !strings.Contains(h.notices[0], want) {
			t.Errorf("notice %q missing %q", h.notices[0], want)
		}
	}
	if !containsLine(logs, "kept local by marker") {
		t.Fatalf("logs = %v", logs)
	}
}

func TestHandleReplyShortLocalNoteDoesNotNotify(t *testing.T) {
	h := &deliveryNoticeHarness{}
	handleReply(context.Background(), Config{EndpointID: "guest"}, &sizeCheckingSender{}, h, peerEvent(),
		"[JKP-LOCAL-ONLY] starting now", func(string, ...any) {})
	if len(h.notices) != 0 {
		t.Fatalf("short local note should not trigger a notice: %v", h.notices)
	}
}

func TestHandleReplyLocalOnlyWithoutNotifierOnlyLogs(t *testing.T) {
	h := &condenseHarness{} // Claude/pi don't expose a local-notice channel.
	var logs []string
	handleReply(context.Background(), Config{EndpointID: "guest"}, &sizeCheckingSender{}, h, peerEvent(),
		"[JKP-LOCAL-ONLY]\n"+strings.Repeat("local ", 100), collect(&logs))
	if len(h.prompts) != 0 || !containsLine(logs, "kept local by marker") {
		t.Fatalf("prompts=%d logs=%v", len(h.prompts), logs)
	}
}

func TestHandleReplyAckOnlyLogsDistinctReason(t *testing.T) {
	var logs []string
	handleReply(context.Background(), Config{EndpointID: "guest"}, &sizeCheckingSender{}, &deliveryNoticeHarness{},
		peerEvent(), "Acknowledged.", collect(&logs))
	if !containsLine(logs, "skipped acknowledgement-only reply") || containsLine(logs, "non-substantive") {
		t.Fatalf("logs = %v", logs)
	}
}
