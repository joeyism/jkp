//go:build integration

package integration

import (
	"context"
	"errors"
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

// muxSender routes an outbound message to the transport that owns the sending
// endpoint. The core takes one Sender; each endpoint has its own IRC client.
type muxSender struct {
	mu     sync.Mutex
	routes map[protocol.EndpointID]core.Sender
}

func newMuxSender() *muxSender {
	return &muxSender{routes: make(map[protocol.EndpointID]core.Sender)}
}

func (m *muxSender) add(id protocol.EndpointID, s core.Sender) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[id] = s
}

func (m *muxSender) Send(msg protocol.OutboundMessage) error {
	m.mu.Lock()
	s, ok := m.routes[msg.EndpointID]
	m.mu.Unlock()
	if !ok {
		return errors.New("no transport for endpoint " + string(msg.EndpointID))
	}
	return s.Send(msg)
}

// TestLivePiAndOpenCodeConversation runs a multi-turn conversation between a
// real pi session and a real OpenCode session, both bound to jkp endpoints on
// real Ergo.
//
// Requires:
//   - Ergo container (JKP_ERGO_ADDR)
//   - OpenCode server (JKP_OC_URL / JKP_OC_PASSWORD)
//   - a pi binary with an anthropic provider (JKP_PI_BIN, default "pi")
//
// The conversation is driven through jkp events because neither harness has an
// outbound "send to peer" tool yet: adapters handle inbound -> reply. This
// still exercises both real models, both adapters, cross-harness delivery, and
// context accumulation within each session.
func TestLivePiAndOpenCodeConversation(t *testing.T) {
	ergo := ergoAddr(t)

	ocURL := os.Getenv("JKP_OC_URL")
	if ocURL == "" {
		ocURL = "http://127.0.0.1:4111"
	}
	ocPass := os.Getenv("JKP_OC_PASSWORD")
	if ocPass == "" {
		t.Skip("set JKP_OC_PASSWORD to run the live pi/OpenCode conversation")
	}
	if !ocReachable(ocURL, ocPass) {
		t.Skipf("OpenCode server not reachable at %s", ocURL)
	}
	piBin := os.Getenv("JKP_PI_BIN")
	if piBin == "" {
		piBin = "pi"
	}
	if !piAvailable(piBin) {
		t.Skipf("pi binary %q not runnable", piBin)
	}

	ensureAccounts(t, ergo, map[string]string{"jpi": "jpi-pw", "joc": "joc-pw", "human": "human-pw"})

	ocSession := createOCSession(t, ocURL, ocPass, "jkp-live-pi-oc")
	piSessionDir := t.TempDir()
	t.Logf("OpenCode session %s | pi session dir %s", ocSession, piSessionDir)

	piClient := irc.NewRealClient(realConfig("jpi", "jpi-pw"))
	ocClient := irc.NewRealClient(realConfig("joc", "joc-pw"))
	// The observer is a real IRC participant that sends every prompt and
	// receives every reply. Replies route to the event sender, so the sender
	// must be a live client -- and its Events() must not be shared with a
	// bridge, or the two consumers race.
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
	mux.add("jpi", senderFunc(func(m protocol.OutboundMessage) error { return piClient.Send(m) }))
	mux.add("joc", senderFunc(func(m protocol.OutboundMessage) error { return ocClient.Send(m) }))

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
		PiBinary: piBin, SessionID: "jkp-pi-live-1", SessionDir: piSessionDir,
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

	t.Log("turn 1 -> pi: establish a codeword")
	piReply := sendAndWait(t, svc, observer, "jpi",
		"Remember this codeword: MAPLE-77. Then reply with exactly: remembered")
	t.Logf("  pi said: %q", piReply)

	t.Log("turn 2 -> pi: recall (session context must accumulate)")
	piRecall := sendAndWait(t, svc, observer, "jpi",
		"What codeword did I give you earlier? Reply with only the codeword.")
	t.Logf("  pi recalled: %q", piRecall)
	if !strings.Contains(piRecall, "MAPLE-77") {
		t.Fatalf("pi session did not retain context across turns, got %q", piRecall)
	}

	t.Log("turn 3 -> opencode: establish a different fact")
	ocReply := sendAndWait(t, svc, observer, "joc",
		"Remember this number: 4242. Reply with exactly: memorized")
	t.Logf("  opencode said: %q", ocReply)

	t.Log("turn 4 -> opencode: recall")
	ocRecall := sendAndWait(t, svc, observer, "joc",
		"What number did I give you earlier? Reply with only the number.")
	t.Logf("  opencode recalled: %q", ocRecall)
	if !strings.Contains(ocRecall, "4242") {
		t.Fatalf("OpenCode session did not retain context across turns, got %q", ocRecall)
	}

	t.Log("turn 5 -> pi: cross-harness content from the OpenCode agent")
	cross := sendAndWait(t, svc, observer, "jpi",
		"Another agent reports its number is 4242. Reply with exactly: acknowledged 4242")
	t.Logf("  pi replied: %q", cross)
	if !strings.Contains(cross, "4242") {
		t.Fatalf("pi did not respond to cross-harness content, got %q", cross)
	}

	t.Log("pi <-> opencode conversation complete")
}

// sendAndWait injects one participant message into an endpoint and waits for
// the harness's reply. The reply routes back to the event sender, which is the
// observer client.
func sendAndWait(t *testing.T, svc *core.Service, observer *irc.RealClient,
	endpoint protocol.EndpointID, body string) string {
	t.Helper()
	ev := protocol.InboundEvent{
		ID:         "evt-" + string(endpoint) + "-" + time.Now().Format("150405.000000000"),
		EndpointID: endpoint, Kind: protocol.KindDM,
		Target: string(endpoint),
		Sender: protocol.Identity{Nick: "human", Account: "human"},
		Body:   body,
	}
	if err := svc.Admit(ev); err != nil {
		t.Fatalf("admit: %v", err)
	}

	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case got := <-observer.Events():
			if got.Kind == protocol.KindDM && got.Sender.Nick == string(endpoint) {
				return got.Body
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("no reply from %s within deadline", endpoint)
	return ""
}

func piAvailable(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, bin, "--version").Run() == nil
}
