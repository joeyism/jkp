package ocadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joey/jkp/internal/jkpclient"
	"github.com/joey/jkp/internal/ocadapter"
)

// fakeOpenCode emulates the OpenCode session API: it assigns its own message
// IDs and links assistant replies through parentID.
type fakeOpenCode struct {
	mu   sync.Mutex
	msgs []map[string]any
	// replyText is returned for each user message, in order.
	replyText []string
	// hold prevents replies from appearing until released (for timeout tests).
	hold bool
}

func (f *fakeOpenCode) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt_async"):
			var body struct {
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			text := ""
			if len(body.Parts) > 0 {
				text = body.Parts[0].Text
			}
			f.appendMsg(t, "user", "", text)
			if !f.hold {
				f.appendReply(t)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
			f.mu.Lock()
			out := make([]map[string]any, len(f.msgs))
			copy(out, f.msgs)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeOpenCode) appendMsg(t *testing.T, role, parentID, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "msg_" + role + "_" + t.Name() + "_" + time.Now().Format("150405.000000000")
	m := map[string]any{
		"info": map[string]any{"id": id, "role": role, "parentID": parentID},
		"parts": []map[string]any{
			{"type": "text", "text": text},
		},
	}
	f.msgs = append(f.msgs, m)
}

// appendReply attaches an assistant reply to the most recent user message,
// exactly as OpenCode links them via parentID.
func (f *fakeOpenCode) appendReply(t *testing.T) {
	f.mu.Lock()
	var lastUser string
	for _, m := range f.msgs {
		info := m["info"].(map[string]any)
		if info["role"] == "user" {
			lastUser = info["id"].(string)
		}
	}
	n := 0
	for _, m := range f.msgs {
		if m["info"].(map[string]any)["role"] == "assistant" {
			n++
		}
	}
	f.mu.Unlock()

	text := "REPLY"
	if n < len(f.replyText) {
		text = f.replyText[n]
	}
	f.mu.Lock()
	id := "msg_asst_" + t.Name() + "_" + time.Now().Format("150405.000000000")
	f.msgs = append(f.msgs, map[string]any{
		"info": map[string]any{"id": id, "role": "assistant", "parentID": lastUser, "finish": "stop"},
		"parts": []map[string]any{
			{"type": "step-start"},
			{"type": "text", "text": text},
		},
	})
	f.mu.Unlock()
}

// fakeJKP records replies the adapter posts back.
type fakeJKP struct {
	mu      sync.Mutex
	replies []map[string]string
	events  chan map[string]any
}

func newFakeJKP() *fakeJKP {
	return &fakeJKP{events: make(chan map[string]any, 8)}
}

func (f *fakeJKP) handler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/events":
			select {
			case ev := <-f.events:
				_ = json.NewEncoder(w).Encode(ev)
			case <-r.Context().Done():
				w.WriteHeader(http.StatusNoContent)
			case <-time.After(200 * time.Millisecond):
				w.WriteHeader(http.StatusNoContent)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/reply":
			var body struct {
				EndpointID string `json:"endpoint_id"`
				InReplyTo  string `json:"in_reply_to"`
				Body       string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.replies = append(f.replies, map[string]string{
				"endpoint_id": body.EndpointID,
				"in_reply_to": body.InReplyTo,
				"body":        body.Body,
			})
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (f *fakeJKP) sent() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]string, len(f.replies))
	copy(out, f.replies)
	return out
}

func startStack(t *testing.T, oc *fakeOpenCode, jkp *fakeJKP, token string) (ocSrv, jkpSrv *httptest.Server) {
	t.Helper()
	ocSrv = httptest.NewServer(oc.handler(t))
	t.Cleanup(ocSrv.Close)
	jkpSrv = httptest.NewServer(jkp.handler(token))
	t.Cleanup(jkpSrv.Close)
	return ocSrv, jkpSrv
}

func makeAdapter(ocURL, jkpURL, token string) *ocadapter.Adapter {
	return ocadapter.New(ocadapter.Config{
		OpenCodeURL:  ocURL,
		Username:     "opencode",
		Password:     "s3cret",
		SessionID:    "ses_bound",
		JKPURL:       jkpURL,
		JKPToken:     token,
		EndpointID:   "agent-a",
		PollInterval: 5 * time.Millisecond,
		ReplyTimeout: 2 * time.Second,
	})
}

func TestInjectAndReplyRoundTrip(t *testing.T) {
	t.Parallel()
	oc := &fakeOpenCode{replyText: []string{"here is my answer"}}
	jkp := newFakeJKP()
	ocSrv, jkpSrv := startStack(t, oc, jkp, "tok")

	a := makeAdapter(ocSrv.URL, jkpSrv.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	jkp.events <- map[string]any{
		"id": "evt-1", "kind": "dm", "target": "agent-a",
		"body":   "why is the sky blue?",
		"sender": map[string]any{"nick": "alice", "account": "alice"},
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := jkp.sent(); len(got) == 1 {
			if got[0]["in_reply_to"] != "evt-1" {
				t.Fatalf("reply must name evt-1, got %v", got[0])
			}
			if got[0]["body"] != "here is my answer" {
				t.Fatalf("unexpected reply body %q", got[0]["body"])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no reply posted back to jkp; got %v", jkp.sent())
}

func TestPromptEmbedsEventMarker(t *testing.T) {
	t.Parallel()
	oc := &fakeOpenCode{}
	jkp := newFakeJKP()
	ocSrv, jkpSrv := startStack(t, oc, jkp, "tok")

	a := makeAdapter(ocSrv.URL, jkpSrv.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	jkp.events <- map[string]any{
		"id": "evt-MARK", "kind": "dm", "target": "agent-a",
		"body": "hello", "sender": map[string]any{"nick": "alice"},
	}
	time.Sleep(300 * time.Millisecond)

	oc.mu.Lock()
	defer oc.mu.Unlock()
	found := false
	for _, m := range oc.msgs {
		if m["info"].(map[string]any)["role"] != "user" {
			continue
		}
		text := m["parts"].([]map[string]any)[0]["text"].(string)
		if strings.Contains(text, ocadapter.EventMarker+"evt-MARK]") {
			if !strings.Contains(text, jkpclient.LocalOnlyMarker) || !strings.Contains(text, "START") || !strings.Contains(text, "16384") {
				t.Fatalf("injected message lacks reply guidance: %q", text)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("submitted prompt must embed the jkp event marker")
	}
}

func TestConcurrentPromptsCorrelateByParentID(t *testing.T) {
	t.Parallel()
	oc := &fakeOpenCode{replyText: []string{"answer-A", "answer-B"}}
	jkp := newFakeJKP()
	ocSrv, jkpSrv := startStack(t, oc, jkp, "tok")

	a := makeAdapter(ocSrv.URL, jkpSrv.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	jkp.events <- map[string]any{
		"id": "evt-A", "kind": "dm", "target": "agent-a",
		"body": "question A", "sender": map[string]any{"nick": "alice"},
	}
	jkp.events <- map[string]any{
		"id": "evt-B", "kind": "dm", "target": "agent-a",
		"body": "question B", "sender": map[string]any{"nick": "alice"},
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		got := jkp.sent()
		if len(got) >= 2 {
			byEvent := map[string]string{}
			for _, r := range got {
				byEvent[r["in_reply_to"]] = r["body"]
			}
			if byEvent["evt-A"] != "answer-A" {
				t.Fatalf("evt-A must get answer-A, got %q", byEvent["evt-A"])
			}
			if byEvent["evt-B"] != "answer-B" {
				t.Fatalf("evt-B must get answer-B, got %q", byEvent["evt-B"])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("both replies never arrived; got %v", jkp.sent())
}

func TestTimeoutIsNotForwardedToPeer(t *testing.T) {
	t.Parallel()
	oc := &fakeOpenCode{hold: true} // assistant never replies
	jkp := newFakeJKP()
	ocSrv, jkpSrv := startStack(t, oc, jkp, "tok")

	a := ocadapter.New(ocadapter.Config{
		OpenCodeURL: ocSrv.URL, Username: "opencode", Password: "s3cret",
		SessionID: "ses_bound", JKPURL: jkpSrv.URL, JKPToken: "tok",
		EndpointID: "agent-a", PollInterval: 5 * time.Millisecond,
		ReplyTimeout: 150 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	jkp.events <- map[string]any{
		"id": "evt-slow", "kind": "dm", "target": "agent-a",
		"body": "hello", "sender": map[string]any{"nick": "alice"},
	}

	time.Sleep(400 * time.Millisecond)
	for _, r := range jkp.sent() {
		if strings.Contains(r["body"], "did not produce a reply") {
			t.Fatalf("timeout must not be forwarded, got %v", r)
		}
	}
}

func TestHandleWaitsPastToolCallPreambleUntilFinalAssistantTurn(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	polls := 0
	userID := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt_async") {
			var body struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode prompt: %v", err)
			}
			mu.Lock()
			userID = "user-1"
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/message") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		polls++
		msgs := []map[string]any{{
			"info":  map[string]any{"id": userID, "role": "user"},
			"parts": []map[string]any{{"type": "text", "text": "[JKP-MSG:evt-final] alice:\n\nquestion"}},
		}}
		msgs = append(msgs, map[string]any{
			"info":  map[string]any{"id": "assistant-tool", "role": "assistant", "parentID": userID, "finish": "tool-calls"},
			"parts": []map[string]any{{"type": "text", "text": "I will inspect the repository first."}},
		})
		if polls == 2 {
			msgs = append(msgs, map[string]any{
				"info":  map[string]any{"id": "assistant-stream", "role": "assistant", "parentID": userID, "finish": ""},
				"parts": []map[string]any{{"type": "text", "text": "Partial streamed result"}},
			})
		}
		if polls >= 3 {
			msgs = append(msgs, map[string]any{
				"info":  map[string]any{"id": "assistant-final", "role": "assistant", "parentID": userID, "finish": "stop"},
				"parts": []map[string]any{{"type": "text", "text": "The completed answer."}},
			})
		}
		_ = json.NewEncoder(w).Encode(msgs)
	}))
	t.Cleanup(server.Close)

	a := ocadapter.New(ocadapter.Config{
		OpenCodeURL: server.URL, Username: "opencode", Password: "s3cret",
		SessionID: "ses_bound", EndpointID: "agent-a", PollInterval: 5 * time.Millisecond,
		ReplyTimeout: time.Second,
	})
	ev := jkpclient.Event{ID: "evt-final", Kind: "dm", Body: "question"}
	ev.Sender.Nick = "alice"
	got, err := a.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if got != "The completed answer." {
		t.Fatalf("Handle returned %q, want completed assistant answer", got)
	}
}

func TestNotifyErrorPostsOneWayPromptWithoutWaitingForAssistant(t *testing.T) {
	t.Parallel()
	oc := &fakeOpenCode{hold: true}
	jkp := newFakeJKP()
	ocSrv, _ := startStack(t, oc, jkp, "tok")
	a := makeAdapter(ocSrv.URL, "", "")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := a.NotifyError(ctx, "[JKP-DELIVERY-ERROR] split the reply"); err != nil {
		t.Fatalf("NotifyError: %v", err)
	}
	oc.mu.Lock()
	defer oc.mu.Unlock()
	if len(oc.msgs) != 1 {
		t.Fatalf("one-way notice should create one user prompt, got %d messages", len(oc.msgs))
	}
	text := oc.msgs[0]["parts"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(text, "[JKP-DELIVERY-ERROR] split the reply") {
		t.Fatalf("notice prompt = %q", text)
	}
}

func TestRunNotifiesOpenCodeWhenSubstantiveTurnIsKeptLocal(t *testing.T) {
	t.Parallel()
	localTurn := "[JKP-LOCAL-ONLY]\nPlan for my user: " + strings.Repeat("next step ", 50)
	oc := &fakeOpenCode{replyText: []string{localTurn, "[JKP-LOCAL-ONLY] notice received"}}
	jkp := newFakeJKP()
	ocSrv, jkpSrv := startStack(t, oc, jkp, "tok")
	a := makeAdapter(ocSrv.URL, jkpSrv.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()
	jkp.events <- map[string]any{
		"id": "evt-local", "kind": "dm", "target": "agent-a", "body": "question",
		"sender": map[string]any{"nick": "alice"},
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		oc.mu.Lock()
		count := len(oc.msgs)
		var notice string
		if count >= 3 {
			notice = oc.msgs[2]["parts"].([]map[string]any)[0]["text"].(string)
		}
		oc.mu.Unlock()
		if count >= 3 {
			if !strings.Contains(notice, "[JKP-NOTICE]") || !strings.Contains(notice, "kept local") || !strings.Contains(notice, "jkp say") {
				t.Fatalf("local-only notice = %q", notice)
			}
			if sent := jkp.sent(); len(sent) != 0 {
				t.Fatalf("local-only turn or one-way notice response escaped to peer: %#v", sent)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("agent did not receive a one-way local-only notice")
}

func TestValidateRejectsIncompleteConfig(t *testing.T) {
	t.Parallel()
	a := ocadapter.New(ocadapter.Config{})
	if err := a.Validate(); err == nil {
		t.Fatal("empty config must fail validation")
	}
}

func TestBasicAuthIsSentToOpenCode(t *testing.T) {
	t.Parallel()
	var authMu sync.Mutex
	gotAuth := ""
	setAuth := func(v string) {
		authMu.Lock()
		defer authMu.Unlock()
		gotAuth = v
	}
	readAuth := func() string {
		authMu.Lock()
		defer authMu.Unlock()
		return gotAuth
	}

	ocSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAuth(r.Header.Get("Authorization"))
		if strings.HasSuffix(r.URL.Path, "/message") {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(ocSrv.Close)

	jkp := newFakeJKP()
	jkpSrv := httptest.NewServer(jkp.handler("tok"))
	t.Cleanup(jkpSrv.Close)

	a := ocadapter.New(ocadapter.Config{
		OpenCodeURL: ocSrv.URL, Username: "opencode", Password: "s3cret",
		SessionID: "ses", JKPURL: jkpSrv.URL, JKPToken: "tok",
		EndpointID: "agent-a", PollInterval: 5 * time.Millisecond,
		ReplyTimeout: 200 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	jkp.events <- map[string]any{
		"id": "evt-a", "kind": "dm", "target": "agent-a",
		"body": "hi", "sender": map[string]any{"nick": "alice"},
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.HasPrefix(readAuth(), "Basic ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("adapter must use HTTP Basic auth toward OpenCode, got %q", readAuth())
}
