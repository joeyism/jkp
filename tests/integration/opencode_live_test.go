//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/localapi"
	"github.com/joey/jkp/internal/ocadapter"
	"github.com/joey/jkp/internal/protocol"
)

// TestLiveOpenCodeEndToEnd drives the full path with real components:
//
//	reviewer IRC client -> Ergo -> jkp bridge/service -> OpenCode adapter
//	  -> a real OpenCode session (model reply) -> jkp -> Ergo -> reviewer
//
// It requires:
//   - the Ergo container from deploy/ergo (JKP_ERGO_ADDR, default 127.0.0.1:6667)
//   - a running OpenCode server (JKP_OC_URL, default http://127.0.0.1:4111)
//     authenticated with JKP_OC_PASSWORD
func TestLiveOpenCodeEndToEnd(t *testing.T) {
	ergo := ergoAddr(t)

	ocURL := os.Getenv("JKP_OC_URL")
	if ocURL == "" {
		ocURL = "http://127.0.0.1:4111"
	}
	ocPass := os.Getenv("JKP_OC_PASSWORD")
	if ocPass == "" {
		t.Skip("set JKP_OC_PASSWORD to run the live OpenCode end-to-end test")
	}
	if !ocReachable(ocURL, ocPass) {
		t.Skipf("OpenCode server not reachable at %s", ocURL)
	}

	ensureAccounts(t, ergo, map[string]string{"jlive": "jlive-pw", "jrev": "jrev-pw"})

	// Bind a real OpenCode session to receive the participant input.
	sessionID := createOCSession(t, ocURL, ocPass, "jkp-live-e2e")
	t.Logf("bound OpenCode session %s", sessionID)

	// jkp core. The sender must actually transmit over IRC, so the endpoint's
	// own IRC client is the transport; a recording wrapper keeps evidence.
	var jkpMu sync.Mutex
	var routed []protocol.OutboundMessage

	agent := irc.NewRealClient(realConfig("jlive", "jlive-pw"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(agent.Connect(context.Background()))
	defer func() { _ = agent.Disconnect() }()

	svc := core.NewService(senderFunc(func(m protocol.OutboundMessage) error {
		jkpMu.Lock()
		routed = append(routed, m)
		jkpMu.Unlock()
		return agent.Send(m) // real IRC transmission
	}), realClock{})

	must(svc.Register(core.Endpoint{
		ID: "jlive", Harness: "opencode", SessionID: sessionID, Nick: "jlive",
	}))
	must(svc.Enable("jlive"))

	br := irc.NewBridge(svc)
	stopAgent := br.Attach("jlive", agent)
	defer stopAgent()

	// The adapter bridges jkp's local API to the real OpenCode session. Serve
	// the local API on a loopback port the adapter polls.
	jkpMu.Lock()
	var svcRef = svc
	jkpMu.Unlock()
	apiSrv := httptest.NewServer(localapi.NewServer(svcRef, "live-token").Handler())
	defer apiSrv.Close()

	ad := ocadapter.New(ocadapter.Config{
		OpenCodeURL:  ocURL,
		Username:     "opencode",
		Password:     ocPass,
		SessionID:    sessionID,
		JKPURL:       apiSrv.URL,
		JKPToken:     "live-token",
		EndpointID:   "jlive",
		PollInterval: 250 * time.Millisecond,
		ReplyTimeout: 90 * time.Second,
	})
	adCtx, adCancel := context.WithCancel(context.Background())
	defer adCancel()
	go func() { _ = ad.Run(adCtx) }()

	// The reviewer is a real IRC participant on real Ergo.
	reviewer := irc.NewRealClient(realConfig("jrev", "jrev-pw"))
	must(reviewer.Connect(context.Background()))
	defer func() { _ = reviewer.Disconnect() }()
	time.Sleep(500 * time.Millisecond)

	// Reviewer DMs the agent's nick. The agent must reach the real model and
	// route its reply back to this reviewer over IRC.
	must(reviewer.Send(protocol.OutboundMessage{
		ID: "out-live-1", EndpointID: "jrev", Kind: protocol.KindDM,
		Target: "jlive", Body: "Please reply with exactly: LIVE-E2E-OK", InReplyTo: "seed",
	}))

	t.Log("waiting for the model reply to travel back over IRC...")
	select {
	case ev := <-reviewer.Events():
		t.Logf("reviewer received: %q (from %s)", ev.Body, ev.Sender.Nick)
		if !strings.Contains(ev.Body, "LIVE-E2E-OK") {
			t.Fatalf("expected model to echo LIVE-E2E-OK, got %q", ev.Body)
		}
		if ev.Sender.Nick != "jlive" {
			t.Fatalf("reply must come from jlive, got %+v", ev.Sender)
		}
	case <-time.After(120 * time.Second):
		t.Fatal("no reply reached the reviewer over IRC")
	}
}

func ocReachable(url, pass string) bool {
	req, err := http.NewRequest(http.MethodGet, url+"/global/health", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth("opencode", pass)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func createOCSession(t *testing.T, url, pass, title string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"title": title})
	req, err := http.NewRequest(http.MethodPost, url+"/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("opencode", pass)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("OpenCode did not return a session id")
	}
	return out.ID
}

var (
	_ = net.Dial
	_ = fmt.Sprintf
)
