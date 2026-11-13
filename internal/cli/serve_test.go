package cli_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/joey/jkp/internal/cli"
	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

const testToken = "cli-test-token"

func TestServeStartsListenerAndAnswersHealth(t *testing.T) {
	t.Parallel()
	svc := core.NewService(discardSender{}, syncClock{})
	var out, errOut bytes.Buffer
	c := cli.New(svc, &out, &errOut)

	ln, err := c.Listen("127.0.0.1:0", testToken)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- c.Serve(ctx, ln, testToken) }()

	healthy := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", "http://"+addr+"/v1/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent {
				healthy = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("serve never answered /v1/health on the bound address")
	}

	// Without the token the same endpoint must refuse.
	req, err := http.NewRequest("GET", "http://"+addr+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("want 401 without token, got %d", resp.StatusCode)
		}
	}

	cancel()
	select {
	case err := <-served:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not shut down after cancel")
	}
}

func TestListenRejectsMissingToken(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	if _, err := c.Listen("127.0.0.1:0", ""); err == nil {
		t.Fatal("listen without a token must fail closed")
	}
}

func TestListenRejectsNonLoopback(t *testing.T) {
	t.Parallel()
	c, _, _ := newCLI(t)
	if _, err := c.Listen("0.0.0.0:0", testToken); err == nil {
		t.Fatal("listen on a non-loopback address must fail closed")
	}
}

func TestServeEndpointsThroughRealListener(t *testing.T) {
	t.Parallel()
	svc := core.NewService(discardSender{}, syncClock{})
	c := cli.New(svc, &bytes.Buffer{}, &bytes.Buffer{})

	ln, err := c.Listen("127.0.0.1:0", testToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Serve(ctx, ln, testToken) }()

	base := "http://" + ln.Addr().String()
	mustPost := func(path, body string) int {
		req, err := http.NewRequest("POST", base+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	if code := mustPost("/v1/register", `{"id":"agent-a","harness":"pi","session_id":"ses","nick":"agent-a"}`); code != http.StatusNoContent {
		t.Fatalf("register: want 204, got %d", code)
	}
	if code := mustPost("/v1/enable", `{"id":"agent-a"}`); code != http.StatusNoContent {
		t.Fatalf("enable: want 204, got %d", code)
	}

	// Inject work and fetch it through the running listener.
	if err := svc.Admit(protocol.InboundEvent{
		ID: "evt-1", EndpointID: "agent-a", Kind: protocol.KindDM,
		Target: "agent-a", Sender: protocol.Identity{Account: "alice"}, Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("GET", base+"/v1/events?endpoint_id=agent-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: want 200, got %d", resp.StatusCode)
	}
}
