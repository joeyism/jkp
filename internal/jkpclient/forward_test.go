package jkpclient_test

import (
	"strings"
	"testing"

	"github.com/joey/jkp/internal/jkpclient"
)

func TestForwardableDropsTimeoutErrors(t *testing.T) {
	msg := "jkp: the bound session did not produce a reply: no assistant reply before timeout"
	if jkpclient.Forwardable(msg) {
		t.Fatal("timeout errors must not be forwarded")
	}
}

func TestClassifyTrailingMarkerIsSignoffAndStillSends(t *testing.T) {
	body := "Yes—approved row 76208118-b180-45c9-b12d-7ae3459c5f2c is already the synthetic pilot in the 54-case suite. Don't add it again.\n\n" +
		strings.Repeat("Provenance detail with “quotes” — and ×. ", 115) +
		"\n\nNo additional action needed from you in reply; these identifiers should let you inspect the live writer. [JKP-NOREPLY]"
	if len(body) < 5000 {
		t.Fatalf("fixture too small: %d bytes", len(body))
	}
	d := jkpclient.Classify(body)
	if d.Action != jkpclient.ActionSend {
		t.Fatalf("trailing marker suppressed a substantive answer: action=%v", d.Action)
	}
	if strings.Contains(strings.ToUpper(d.Body), "JKP-NOREPLY") {
		t.Fatalf("marker must be stripped from sent body: %q", d.Body[len(d.Body)-120:])
	}
	if !strings.HasSuffix(d.Body, "inspect the live writer.") {
		t.Fatalf("body tail = %q", d.Body[len(d.Body)-60:])
	}
}

func TestClassifyMidTextMarkerIsStrippedAndSent(t *testing.T) {
	text := "Assessment for my user.\n\nTo the guest: thanks—the provenance corrections resolve the main uncertainties. [JKP-NOREPLY]\n\nNext I'll inspect the published inputs. " + strings.Repeat("This includes the row provenance, writer and fixture status. ", 8)
	d := jkpclient.Classify(text)
	if d.Action != jkpclient.ActionSend || strings.Contains(d.Body, "[JKP-NOREPLY]") {
		t.Fatalf("decision = %#v", d)
	}
}

func TestClassifyLeadingMarkerKeepsTurnLocal(t *testing.T) {
	for _, text := range []string{
		"[JKP-LOCAL-ONLY]\nStarting the dataset edits now. " + strings.Repeat("plan ", 80),
		"  [jkp-local-only] plan for my user",
		"`[JKP-LOCAL-ONLY]` plan for my user",
		"**[JKP-LOCAL-ONLY]** plan for my user",
		"[JKP-NOREPLY]\nlegacy alias still works at the start",
	} {
		d := jkpclient.Classify(text)
		if d.Action != jkpclient.ActionLocalOnly {
			t.Fatalf("leading marker not honored for %q: %#v", text, d)
		}
		if strings.Contains(strings.ToUpper(d.Body), "JKP-") {
			t.Fatalf("local body still contains marker: %q", d.Body)
		}
	}
}

func TestClassifyMarkerOnlyTurnDoesNotSend(t *testing.T) {
	for _, text := range []string{"[JKP-NOREPLY]", "  `[JKP-LOCAL-ONLY]`  ", "\n[jkp-noreply]\n"} {
		if d := jkpclient.Classify(text); d.Action == jkpclient.ActionSend {
			t.Fatalf("marker-only turn would be sent: %q -> %#v", text, d)
		}
	}
}

func TestClassifyPreservesExistingFilters(t *testing.T) {
	if d := jkpclient.Classify("Acknowledged."); d.Action != jkpclient.ActionAckOnly {
		t.Fatalf("ack = %#v", d)
	}
	if d := jkpclient.Classify("jkp: the bound session did not produce a reply: timeout"); d.Action == jkpclient.ActionSend {
		t.Fatalf("timeout text = %#v", d)
	}
	if d := jkpclient.Classify("   "); d.Action != jkpclient.ActionEmpty {
		t.Fatalf("empty = %#v", d)
	}
}

func TestForwardableHonorsOnlyLeadingMarker(t *testing.T) {
	if jkpclient.Forwardable("[JKP-LOCAL-ONLY]\nlocal plan " + strings.Repeat("x ", 200)) {
		t.Fatal("leading marker turn was forwardable")
	}
	if !jkpclient.Forwardable("Real answer for the peer. " + strings.Repeat("detail ", 50) + "[JKP-NOREPLY]") {
		t.Fatal("trailing sign-off marker made a substantive answer non-forwardable")
	}
}

func TestInboundGuidanceDocumentsLocalOnlyAndByteLimit(t *testing.T) {
	text := jkpclient.InboundGuidance()
	if !strings.Contains(text, jkpclient.LocalOnlyMarker) || !strings.Contains(text, "START") || !strings.Contains(text, "16384") {
		t.Fatalf("inbound guidance missing local-only marker or byte limit: %q", text)
	}
	if strings.Contains(text, jkpclient.NoReplyMarker) {
		t.Fatalf("guidance should not teach ambiguous legacy sign-off marker: %q", text)
	}
}

func TestLocalOnlyNoticeGivesExplicitRecovery(t *testing.T) {
	notice := jkpclient.LocalOnlyNotice("host", 5098)
	for _, want := range []string{"[JKP-NOTICE]", "5098 bytes", "[JKP-LOCAL-ONLY]", "NOT sent to host", "jkp say"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice %q missing %q", notice, want)
		}
	}
	if strings.Contains(notice, "[JKP-NOREPLY]") {
		t.Fatalf("notice includes ambiguous legacy marker: %q", notice)
	}
}

func TestForwardableDropsAckOnlyReplies(t *testing.T) {
	acks := []string{
		"Acknowledged.",
		"Understood.",
		"Thanks—no outstanding upstream questions right now.",
		"Acknowledged—upstream review complete.",
		"I'm available if the Delorean planning turns up any upstream questions.",
	}
	for _, msg := range acks {
		if jkpclient.Forwardable(msg) {
			t.Fatalf("ACK-only reply was forwarded: %q", msg)
		}
	}
}

func TestForwardableKeepsSubstantiveReplies(t *testing.T) {
	keep := []string{
		"Confirmed: Gold videos[] schema is ARRAY<STRUCT<gcs_path STRING>>. Hash format is md5:<hex>.",
		"The plan matches, but SQL and Python must select the same video. Does Delorean use index 0?",
		"Both normal discovery paths currently select video_gcs_paths[0]. Hash resolution must follow the selected path.",
	}
	for _, msg := range keep {
		if !jkpclient.Forwardable(msg) {
			t.Fatalf("substantive reply was dropped: %q", msg)
		}
	}
}
