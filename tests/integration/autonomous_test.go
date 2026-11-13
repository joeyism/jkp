//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/localapi"
	"github.com/joey/jkp/internal/ocadapter"
	"github.com/joey/jkp/internal/piadapter"
	"github.com/joey/jkp/internal/protocol"
)

// TestLiveAutonomousAgentToAgent proves one agent can initiate to another
// without a human driving each hop. pi is asked to consult the OpenCode agent;
// pi emits a [jkp-send] directive, jkp routes it to the OpenCode endpoint, and
// the OpenCode answer is delivered back into pi's session.
//
// This is the outbound path: adapters handle inbound -> reply, and a directive
// in the harness reply turns into a new message to a peer.
func TestLiveAutonomousAgentToAgent(t *testing.T) {
	ergo := ergoAddr(t)

	ocURL := envOr("JKP_OC_URL", "http://127.0.0.1:4111")
	ocPass := os.Getenv("JKP_OC_PASSWORD")
	if ocPass == "" {
		t.Skip("set JKP_OC_PASSWORD to run the autonomous agent-to-agent test")
	}
	if !ocReachable(ocURL, ocPass) {
		t.Skipf("OpenCode server not reachable at %s", ocURL)
	}
	piBin := envOr("JKP_PI_BIN", "pi")
	if !piAvailable(piBin) {
		t.Skipf("pi binary %q not runnable", piBin)
	}

	ensureAccounts(t, ergo, map[string]string{"jpi": "jpi-pw", "joc": "joc-pw", "human": "human-pw"})

	ocSession := createOCSession(t, ocURL, ocPass, "jkp-live-autonomous")
	piSessionDir := t.TempDir()

	piClient := irc.NewRealClient(realConfig("jpi", "jpi-pw"))
	ocClient := irc.NewRealClient(realConfig("joc", "joc-pw"))
	observer := irc.NewRealClient(realConfig("human", "human-pw"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(piClient.Connect(context.Background()))
	defer func() { _ = piClient.Disconnect() }()
	must(ocClient.Connect(context.Background()))
	defer func() { _ = ocClient.Disconnect() }()
	must(observer.Connect(context.Background()))
	defer func() { _ = observer.Disconnect() }()

	mux := newMuxSender()
	tap := &recordingSender{}
	mux.add("jpi", senderFunc(func(m protocol.OutboundMessage) error {
		tap.record("jpi", m)
		return piClient.Send(m)
	}))
	mux.add("joc", senderFunc(func(m protocol.OutboundMessage) error {
		tap.record("joc", m)
		return ocClient.Send(m)
	}))

	svc := core.NewService(mux, realClock{})
	must(svc.Register(core.Endpoint{ID: "jpi", Harness: "pi", SessionID: "pi-live", Nick: "jpi"}))
	must(svc.Register(core.Endpoint{ID: "joc", Harness: "opencode", SessionID: ocSession, Nick: "joc"}))
	must(svc.Enable("jpi"))
	must(svc.Enable("joc"))

	br := irc.NewBridge(svc)
	stopPi := br.Attach("jpi", piClient)
	defer stopPi()
	stopOc := br.Attach("joc", ocClient)
	defer stopOc()

	apiSrv := httptest.NewServer(localapi.NewServer(svc, "live-token").Handler())
	defer apiSrv.Close()

	piAd := piadapter.New(piadapter.Config{
		PiBinary: piBin, SessionID: "jkp-pi-autonomous-1", SessionDir: piSessionDir,
		Provider: "anthropic", Model: "anthropic/claude-sonnet-4-5",
		JKPURL: apiSrv.URL, JKPToken: "live-token", EndpointID: "jpi",
		TurnTimeout: 120 * time.Second,
	})
	ocAd := ocadapter.New(ocadapter.Config{
		OpenCodeURL: ocURL, Username: "opencode", Password: ocPass,
		SessionID: ocSession, JKPURL: apiSrv.URL, JKPToken: "live-token",
		EndpointID: "joc", PollInterval: 250 * time.Millisecond,
		ReplyTimeout: 120 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = piAd.Run(ctx) }()
	go func() { _ = ocAd.Run(ctx) }()
	time.Sleep(500 * time.Millisecond)

	// Ask pi to consult the OpenCode agent. pi must decide to use the
	// directive; no human sends anything to joc.
	t.Log("human -> pi: consult the OpenCode agent")
	ack := sendAndWait(t, svc, observer, "jpi",
		"Please ask the agent named 'joc' to choose a colour. Use the [jkp-send to=joc] directive to ask it, and tell me you have done so.")
	t.Logf("  pi replied to human: %q", ack)

	// pi's directive must have produced a message that reached OpenCode's
	// bound session. Watch the session until a peer message appears.
	t.Log("waiting for the peer message to reach the OpenCode session...")
	peerMsg := waitForOCMessage(t, ocURL, ocPass, ocSession, 60*time.Second, func(text string) bool {
		return strings.Contains(text, "colour") || strings.Contains(text, "color")
	})
	t.Logf("  OpenCode session received from a peer: %q", truncate(peerMsg, 120))

	// OpenCode must answer pi. We assert on routed transport messages rather
	// than reading pi's session: the adapter is actively using that session,
	// and a second pi process would race it. The pi -> OpenCode leg is already
	// proven above by the peer message appearing in the OpenCode session.
	t.Log("waiting for OpenCode's answer to route back to pi...")
	backToPi := waitForRouted(t, tap, 120*time.Second, func(from, to string, body string) bool {
		return from == "joc" && to == "jpi"
	})
	t.Logf("  OpenCode -> pi: %q", truncate(backToPi, 120))

	t.Log("autonomous agent-to-agent exchange complete")
	_ = piSessionDir
}

// waitForOCMessage polls the OpenCode session for a message containing text
// the predicate accepts. It reads the real session transcript.
func waitForOCMessage(t *testing.T, url, pass, session string, timeout time.Duration,
	accept func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, text := range ocSessionTexts(t, url, pass, session) {
			if accept(text) {
				return text
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("peer message never reached the OpenCode session")
	return ""
}

func ocSessionTexts(t *testing.T, url, pass, session string) []string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/session/%s/message", url, session), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("opencode", pass)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var msgs []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil
	}
	var out []string
	for _, m := range msgs {
		var b strings.Builder
		for _, p := range m.Parts {
			if p.Type == "text" && p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// recordingSender captures every routed outbound message with its owning
// endpoint, so a test can assert on the exchange without observing a harness
// session that an adapter is concurrently using.
type recordingSender struct {
	mu   sync.Mutex
	sent []routedMessage
}

type routedMessage struct {
	From string
	To   string
	Body string
}

func (r *recordingSender) record(from string, m protocol.OutboundMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, routedMessage{From: from, To: m.Target, Body: m.Body})
}

func (r *recordingSender) find(accept func(from, to, body string) bool) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.sent {
		if accept(m.From, m.To, m.Body) {
			return m.Body, true
		}
	}
	return "", false
}

func waitForRouted(t *testing.T, tap *recordingSender, timeout time.Duration,
	accept func(from, to, body string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if body, ok := tap.find(accept); ok {
			return body
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("expected routed message never appeared")
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// runCapture runs a command and returns its stdout.
func runCapture(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
