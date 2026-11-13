package jkpclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/joey/jkp/internal/jkpclient"
)

type recordingJKP struct {
	mu      sync.Mutex
	replies []map[string]string
	sends   []map[string]string
}

func (r *recordingJKP) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case strings.HasSuffix(req.URL.Path, "/reply"):
			r.replies = append(r.replies, body)
		case strings.HasSuffix(req.URL.Path, "/send"):
			r.sends = append(r.sends, body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (r *recordingJKP) snapshot() ([]map[string]string, []map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := append([]map[string]string(nil), r.replies...)
	snd := append([]map[string]string(nil), r.sends...)
	return rep, snd
}

func start(t *testing.T) (*recordingJKP, *jkpclient.Client) {
	t.Helper()
	rec := &recordingJKP{}
	srv := httptest.NewServer(rec.handler("tok"))
	t.Cleanup(srv.Close)
	return rec, jkpclient.New(srv.URL, "tok", "agent-a")
}

func TestDispatchSendsToPeerAndRepliesToSender(t *testing.T) {
	t.Parallel()
	rec, c := start(t)
	if err := c.DispatchReply(context.Background(), "evt-1",
		"[jkp-send to=joc]please review the cache path[/jkp-send]\nI have asked the other agent."); err != nil {
		t.Fatal(err)
	}
	replies, sends := rec.snapshot()
	if len(sends) != 1 || sends[0]["to_endpoint_id"] != "joc" {
		t.Fatalf("want one send to joc, got %+v", sends)
	}
	if sends[0]["body"] != "please review the cache path" {
		t.Fatalf("unexpected send body %q", sends[0]["body"])
	}
	if sends[0]["from_endpoint_id"] != "agent-a" {
		t.Fatalf("send must be attributed to agent-a, got %+v", sends[0])
	}
	if len(replies) != 1 || replies[0]["body"] != "I have asked the other agent." {
		t.Fatalf("unexpected replies %+v", replies)
	}
	if replies[0]["in_reply_to"] != "evt-1" {
		t.Fatalf("reply must name evt-1, got %+v", replies[0])
	}
}

func TestDispatchAcksSenderWhenOnlyDelegating(t *testing.T) {
	t.Parallel()
	rec, c := start(t)
	if err := c.DispatchReply(context.Background(), "evt-2",
		"[jkp-send to=bob]take a look[/jkp-send]"); err != nil {
		t.Fatal(err)
	}
	replies, sends := rec.snapshot()
	if len(sends) != 1 {
		t.Fatalf("want one send, got %+v", sends)
	}
	if len(replies) != 1 {
		t.Fatalf("sender must not be left waiting, got %+v", replies)
	}
	if !strings.Contains(replies[0]["body"], "forwarded your message to bob") {
		t.Fatalf("unexpected ack %q", replies[0]["body"])
	}
}

func TestDispatchPlainReplyDoesNotSend(t *testing.T) {
	t.Parallel()
	rec, c := start(t)
	if err := c.DispatchReply(context.Background(), "evt-3", "the cache is fine"); err != nil {
		t.Fatal(err)
	}
	replies, sends := rec.snapshot()
	if len(sends) != 0 {
		t.Fatalf("plain text must not send to peers, got %+v", sends)
	}
	if len(replies) != 1 || replies[0]["body"] != "the cache is fine" {
		t.Fatalf("unexpected replies %+v", replies)
	}
}

func TestDispatchNoReplyMarkerSendsNothing(t *testing.T) {
	t.Parallel()
	rec, c := start(t)
	if err := c.DispatchReply(context.Background(), "evt-no-reply", "[JKP-NOREPLY]\n[jkp-send to=other]do not send[/jkp-send]\nStarting local work now."); err != nil {
		t.Fatal(err)
	}
	replies, sends := rec.snapshot()
	if len(replies) != 0 || len(sends) != 0 {
		t.Fatalf("no-reply turn emitted replies=%v sends=%v", replies, sends)
	}
}

func TestDispatchTrailingMarkerStillRepliesWithMarkerStripped(t *testing.T) {
	t.Parallel()
	rec, c := start(t)
	body := "The pilot row is already in the suite. " + strings.Repeat("detail ", 60) + "[JKP-NOREPLY]"
	if err := c.DispatchReply(context.Background(), "evt-tail", body); err != nil {
		t.Fatal(err)
	}
	replies, _ := rec.snapshot()
	if len(replies) != 1 || strings.Contains(replies[0]["body"], "JKP-") {
		t.Fatalf("replies = %#v", replies)
	}
}

func TestDispatchMultipleSends(t *testing.T) {
	t.Parallel()
	rec, c := start(t)
	if err := c.DispatchReply(context.Background(), "evt-4",
		"[jkp-send to=a]one[/jkp-send][jkp-send to=b]two[/jkp-send]\ndone"); err != nil {
		t.Fatal(err)
	}
	_, sends := rec.snapshot()
	if len(sends) != 2 {
		t.Fatalf("want two sends, got %+v", sends)
	}
}
