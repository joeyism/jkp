package localapi_test

import (
	"net/http"
	"testing"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

// TestSendInitiatesToPeer covers the agent-initiated outbound path: one
// endpoint's adapter asks jkp to deliver a new message to another endpoint,
// rather than replying to whoever addressed it.
func TestSendInitiatesToPeer(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "sa", Nick: "agent-a"})
	_ = svc.Register(core.Endpoint{ID: "agent-b", Harness: "opencode", SessionID: "sb", Nick: "agent-b"})
	_ = svc.Enable("agent-a")
	_ = svc.Enable("agent-b")

	rec := doJSON(t, h, "POST", "/v1/send", testToken, map[string]string{
		"from_endpoint_id": "agent-a",
		"to_endpoint_id":   "agent-b",
		"body":             "I found a race in the retry loop",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("send: want 204, got %d %s", rec.Code, rec.Body)
	}

	ev, ok := svc.Next("agent-b")
	if !ok {
		t.Fatal("the target endpoint must receive the message")
	}
	if ev.Body != "I found a race in the retry loop" {
		t.Fatalf("unexpected body %q", ev.Body)
	}
	// The sender is the initiating endpoint, so a reply routes back to it.
	if ev.Sender.Nick != "agent-a" {
		t.Fatalf("sender must be agent-a, got %+v", ev.Sender)
	}
	if ev.ReplyTarget() != "agent-a" {
		t.Fatalf("reply must route back to agent-a, got %q", ev.ReplyTarget())
	}
}

func TestSendRejectsUnknownEndpoint(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "sa", Nick: "agent-a"})
	_ = svc.Enable("agent-a")

	rec := doJSON(t, h, "POST", "/v1/send", testToken, map[string]string{
		"from_endpoint_id": "agent-a",
		"to_endpoint_id":   "ghost",
		"body":             "hi",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for unknown target, got %d", rec.Code)
	}
}

func TestSendRejectsSelf(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "sa", Nick: "agent-a"})
	_ = svc.Enable("agent-a")

	rec := doJSON(t, h, "POST", "/v1/send", testToken, map[string]string{
		"from_endpoint_id": "agent-a",
		"to_endpoint_id":   "agent-a",
		"body":             "loop",
	})
	// Self-addressed messages must not create a self-triggering loop.
	if rec.Code == http.StatusNoContent {
		t.Fatal("an endpoint must not be able to message itself")
	}
}

func TestSendRequiresBody(t *testing.T) {
	t.Parallel()
	svc, h := newTestServer(t)
	_ = svc.Register(core.Endpoint{ID: "agent-a", Harness: "pi", SessionID: "sa", Nick: "agent-a"})
	_ = svc.Register(core.Endpoint{ID: "agent-b", Harness: "pi", SessionID: "sb", Nick: "agent-b"})
	_ = svc.Enable("agent-a")
	_ = svc.Enable("agent-b")

	rec := doJSON(t, h, "POST", "/v1/send", testToken, map[string]string{
		"from_endpoint_id": "agent-a", "to_endpoint_id": "agent-b",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for empty body, got %d", rec.Code)
	}
}

func TestSendUnauthenticatedRejected(t *testing.T) {
	t.Parallel()
	_, h := newTestServer(t)
	rec := doJSON(t, h, "POST", "/v1/send", "", map[string]string{
		"from_endpoint_id": "agent-a", "to_endpoint_id": "agent-b", "body": "x",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	_ = protocol.KindDM
}
