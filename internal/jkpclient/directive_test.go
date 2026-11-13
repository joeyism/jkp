package jkpclient_test

import (
	"testing"

	"github.com/joey/jkp/internal/jkpclient"
)

func TestParseExtractsSendDirective(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply(`[jkp-send to=joc]
I found a race in the retry loop
[/jkp-send]`)
	if len(out.Sends) != 1 {
		t.Fatalf("want 1 send, got %d", len(out.Sends))
	}
	if out.Sends[0].To != "joc" {
		t.Fatalf("want target joc, got %q", out.Sends[0].To)
	}
	if out.Sends[0].Body != "I found a race in the retry loop" {
		t.Fatalf("unexpected body %q", out.Sends[0].Body)
	}
	if out.Reply != "" {
		t.Fatalf("no leftover reply expected, got %q", out.Reply)
	}
}

func TestParseSeparatesSendFromReply(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply(`[jkp-send to=joc]please review the cache path[/jkp-send]
I have asked the other agent.`)
	if len(out.Sends) != 1 {
		t.Fatalf("want 1 send, got %d", len(out.Sends))
	}
	if out.Sends[0].Body != "please review the cache path" {
		t.Fatalf("unexpected body %q", out.Sends[0].Body)
	}
	if out.Reply != "I have asked the other agent." {
		t.Fatalf("unexpected reply %q", out.Reply)
	}
}

func TestParseHandlesMultipleSends(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply(`[jkp-send to=alice]one[/jkp-send]
[jkp-send to=bob]two[/jkp-send]
done`)
	if len(out.Sends) != 2 {
		t.Fatalf("want 2 sends, got %d", len(out.Sends))
	}
	if out.Sends[0].To != "alice" || out.Sends[1].To != "bob" {
		t.Fatalf("unexpected targets %q %q", out.Sends[0].To, out.Sends[1].To)
	}
	if out.Reply != "done" {
		t.Fatalf("unexpected reply %q", out.Reply)
	}
}

func TestParsePlainReplyHasNoSends(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply("The cache is correct; see `getOrFill`.")
	if len(out.Sends) != 0 {
		t.Fatalf("plain text must not produce sends, got %+v", out.Sends)
	}
	if out.Reply != "The cache is correct; see `getOrFill`." {
		t.Fatalf("reply must be preserved verbatim, got %q", out.Reply)
	}
}

func TestParseSupportsQuotedTarget(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply(`[jkp-send to="oc-reviewer"]hello there[/jkp-send]`)
	if len(out.Sends) != 1 || out.Sends[0].To != "oc-reviewer" {
		t.Fatalf("quoted target not parsed: %+v", out.Sends)
	}
}

func TestParseIgnoresUnclosedDirective(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply(`[jkp-send to=joc]
this never closes`)
	if len(out.Sends) != 0 {
		t.Fatalf("unclosed directive must not produce a send, got %+v", out.Sends)
	}
	// The raw text is preserved as the reply so nothing is silently dropped.
	if out.Reply == "" {
		t.Fatal("unclosed directive text must survive as the reply")
	}
}

func TestParseBodyPreservesInnerLines(t *testing.T) {
	t.Parallel()
	out := jkpclient.ParseReply(`[jkp-send to=peer]
line one
line two
[/jkp-send]`)
	if len(out.Sends) != 1 {
		t.Fatalf("want 1 send, got %d", len(out.Sends))
	}
	if out.Sends[0].Body != "line one\nline two" {
		t.Fatalf("inner lines must be preserved, got %q", out.Sends[0].Body)
	}
}

func TestParseIsCaseSensitiveOnMarker(t *testing.T) {
	t.Parallel()
	// The marker is a protocol literal; a model that writes it differently is
	// not silently reinterpreted as a command.
	out := jkpclient.ParseReply(`[JKP-SEND to=joc]x[/JKP-SEND]`)
	if len(out.Sends) != 0 {
		t.Fatalf("altered marker must not be honored, got %+v", out.Sends)
	}
}

func TestLeadingLocalOnlySuppressesPeerSendDirectives(t *testing.T) {
	parsed := jkpclient.ParseReply("[JKP-LOCAL-ONLY]\n[jkp-send to=bob]do not send this[/jkp-send]\nlocal plan")
	if len(parsed.Sends) != 0 || !parsed.LocalOnly {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestTrailingMarkerDoesNotSuppressDirectivesOrReply(t *testing.T) {
	parsed := jkpclient.ParseReply("[jkp-send to=bob]please review[/jkp-send]\nAnswer for alice. [JKP-NOREPLY]")
	if len(parsed.Sends) != 1 || parsed.LocalOnly || parsed.Reply != "Answer for alice." {
		t.Fatalf("parsed = %#v", parsed)
	}
}
