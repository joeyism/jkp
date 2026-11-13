package localapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/localapi"
	"github.com/joey/jkp/internal/protocol"
)

type syncClock struct{}

func (syncClock) Now() time.Time { return time.Now() }

type discardSender struct{}

func (discardSender) Send(protocol.OutboundMessage) error { return nil }

const testToken = "test-local-token"

func newTestServer(t *testing.T) (*core.Service, http.Handler) {
	t.Helper()
	svc := core.NewService(discardSender{}, syncClock{})
	srv := localapi.NewServer(svc, testToken)
	return svc, srv.Handler()
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMissingTokenReturns401(t *testing.T) {
	t.Parallel()
	_, h := newTestServer(t)
	rec := doJSON(t, h, "GET", "/v1/events?endpoint_id=agent-a", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestWrongTokenReturns401(t *testing.T) {
	t.Parallel()
	_, h := newTestServer(t)
	rec := doJSON(t, h, "GET", "/v1/events?endpoint_id=agent-a", "wrong", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestRegisterEnableAndReceive(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)

	rec := doJSON(t, h, "POST", "/v1/register", testToken, map[string]string{
		"id": "agent-a", "harness": "pi", "session_id": "ses_a", "nick": "agent-a",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("register: want 204, got %d %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, "POST", "/v1/enable", testToken, map[string]string{"id": "agent-a"}); rec.Code != http.StatusNoContent {
		t.Fatalf("enable: want 204, got %d", rec.Code)
	}

	// Inject an inbound event through the service directly.
	if err := svc.Admit(protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "alice"}, Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}

	rec = doJSON(t, h, "GET", "/v1/events?endpoint_id=agent-a", testToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("events: want 200, got %d %s", rec.Code, rec.Body)
	}
	var ev protocol.InboundEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ID != "evt-1" || ev.Body != "hi" {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestLongPollTimesOutWith204(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "s", Nick: "agent-a"})
	_ = svc.Enable("agent-a")

	srv := localapi.NewServer(svc, testToken)
	srv.LongPollTimeout = 20 * time.Millisecond

	req := httptest.NewRequest("GET", "/v1/events?endpoint_id=agent-a", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204 on timeout, got %d", rec.Code)
	}
	_ = h
}

func TestReplyWithoutInReplyToReturns400(t *testing.T) {
	t.Parallel()
	_, h := newTestServer(t)
	rec := doJSON(t, h, "POST", "/v1/reply", testToken, map[string]string{
		"endpoint_id": "agent-a", "body": "hi",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body)
	}
}

func TestReplyDerivesConversationFromInboundEvent(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "s", Nick: "agent-a"})
	_ = svc.Enable("agent-a")
	if err := svc.Admit(protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "alice"}, Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, h, "POST", "/v1/reply", testToken, map[string]string{
		"endpoint_id": "agent-a", "in_reply_to": "evt-1", "body": "pong",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reply: want 204, got %d %s", rec.Code, rec.Body)
	}
}

func TestReplyToUnknownEventReturns404(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "s", Nick: "agent-a"})
	_ = svc.Enable("agent-a")

	rec := doJSON(t, h, "POST", "/v1/reply", testToken, map[string]string{
		"endpoint_id": "agent-a", "in_reply_to": "evt-nope", "body": "x",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d %s", rec.Code, rec.Body)
	}
}

func TestFinishEndpoint(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "s", Nick: "agent-a"})
	_ = svc.Enable("agent-a")

	rec := doJSON(t, h, "POST", "/v1/finish", testToken, map[string]string{
		"endpoint_id": "agent-a", "kind": "dm", "target": "alice",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("finish: want 204, got %d %s", rec.Code, rec.Body)
	}
	if svc.Status(protocol.Conversation{Kind: protocol.KindDM, Target: "alice"}) != core.StatusFinished {
		t.Fatal("conversation must be finished")
	}
}

func TestDefaultListenAddrIsLoopback(t *testing.T) {
	t.Parallel()
	if !localapi.DefaultListenAddrIsLoopback() {
		t.Fatal("default listen address must be loopback-only")
	}
}
