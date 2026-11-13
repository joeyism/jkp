//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

// TestCrossHostTLSAndProcessIsolation treats the Ergo container as a second
// machine: it has its own IP, its own network namespace, and its own process.
// The jkp side runs two separate endpoints with independent state and
// transports, connected over TLS to a non-loopback address.
//
// This proves the properties a two-machine LAN needs:
//   - traffic crosses a real interface (not the loopback shortcut)
//   - the wire is encrypted
//   - one endpoint's disappearance does not corrupt the other
//   - a reconnect does not replay history as new instructions
//
// Run with JKP_ERGO_TLS_ADDR set to the server's routable address, e.g.
//
//	JKP_ERGO_TLS_ADDR=172.20.0.2:6697 go test -tags=integration -run CrossHost ...
func TestCrossHostTLSAndProcessIsolation(t *testing.T) {
	addr := os.Getenv("JKP_ERGO_TLS_ADDR")
	if addr == "" {
		t.Skip("set JKP_ERGO_TLS_ADDR (e.g. 172.20.0.2:6697) to run the cross-host TLS test")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("bad JKP_ERGO_TLS_ADDR: %v", err)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		t.Skipf("JKP_ERGO_TLS_ADDR %s is loopback; use the server's routable address", addr)
	}
	port := 0
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil || port == 0 {
		t.Fatalf("bad port in %q", addr)
	}

	if err := waitTCP(addr, 15*time.Second); err != nil {
		t.Skipf("Ergo not reachable at %s: %v", addr, err)
	}

	ensureAccounts(t, "127.0.0.1:6667", map[string]string{"xhost": "xhost-pw", "xpeer": "xpeer-pw"})

	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // self-signed test server; pin for real use
	}

	// Two independent endpoints, each with its own transport and its own
	// participation state -- as separate processes would be.
	mkClient := func(nick, pass string) *irc.RealClient {
		return irc.NewRealClient(irc.RealConfig{
			Host: host, Port: port, TLS: true, TLSConfig: tlsCfg,
			Nick: nick, Account: nick, Password: pass,
		})
	}
	a := mkClient("xhost", "xhost-pw")
	b := mkClient("xpeer", "xpeer-pw")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.Connect(context.Background()))
	must(b.Connect(context.Background()))

	mux := newMuxSender()
	mux.add("xhost", senderFunc(func(m protocol.OutboundMessage) error { return a.Send(m) }))
	mux.add("xpeer", senderFunc(func(m protocol.OutboundMessage) error { return b.Send(m) }))

	svc := core.NewService(mux, realClock{})
	must(svc.Register(core.Endpoint{ID: "xhost", Harness: "pi", SessionID: "s1", Nick: "xhost"}))
	must(svc.Register(core.Endpoint{ID: "xpeer", Harness: "opencode", SessionID: "s2", Nick: "xpeer"}))
	must(svc.Enable("xhost"))
	must(svc.Enable("xpeer"))

	br := irc.NewBridge(svc)
	stopA := br.Attach("xhost", a)
	defer stopA()
	stopB := br.Attach("xpeer", b)
	defer stopB()
	time.Sleep(400 * time.Millisecond)

	// Cross-host message over TLS.
	t.Log("cross-host TLS message")
	must(b.Send(protocol.OutboundMessage{
		ID: "x1", EndpointID: "xpeer", Kind: protocol.KindDM,
		Target: "xhost", Body: "hello across the wire", InReplyTo: "seed",
	}))
	ev := waitEvent(t, svc, "xhost", 15*time.Second)
	if ev.Body != "hello across the wire" {
		t.Fatalf("unexpected body %q", ev.Body)
	}
	t.Logf("  received over TLS from %s: %q", ev.Sender.Nick, ev.Body)

	// One side disappears. The other must keep working.
	t.Log("one endpoint disconnects")
	must(b.Disconnect())
	time.Sleep(300 * time.Millisecond)
	must(a.Send(protocol.OutboundMessage{
		ID: "x2", EndpointID: "xhost", Kind: protocol.KindDM,
		Target: "xpeer", Body: "are you there?", InReplyTo: "x1",
	}))
	// xpeer is gone; it must not fabricate a delivery for its own endpoint.
	if _, ok := svc.Next("xpeer"); ok {
		t.Fatal("a disconnected endpoint must not gain deliveries")
	}
	t.Log("  surviving endpoint continued cleanly")

	// Reconnect the departed side. A replay must not surface as new work.
	// Give the server a moment to release the nick held by the closed session.
	t.Log("endpoint reconnects")
	time.Sleep(2 * time.Second)
	b2 := mkClient("xpeer", "xpeer-pw")
	must(b2.Connect(context.Background()))
	defer func() { _ = b2.Disconnect() }()
	stopB2 := br.Attach("xpeer", b2)
	defer stopB2()
	time.Sleep(400 * time.Millisecond)

	if _, ok := svc.Next("xpeer"); ok {
		t.Fatal("reconnect must not replay history as new instructions")
	}
	t.Log("  reconnect produced no replayed work")

	// The reconnected side can converse again.
	must(b2.Send(protocol.OutboundMessage{
		ID: "x3", EndpointID: "xpeer", Kind: protocol.KindDM,
		Target: "xhost", Body: "I am back", InReplyTo: "x2",
	}))
	ev2 := waitEvent(t, svc, "xhost", 15*time.Second)
	if ev2.Body != "I am back" {
		t.Fatalf("unexpected body %q", ev2.Body)
	}
	t.Logf("  conversation resumed: %q", ev2.Body)

	t.Log("cross-host TLS + process isolation verified")
}

func waitTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("unreachable")
}

func waitEvent(t *testing.T, svc *core.Service, id protocol.EndpointID, timeout time.Duration) protocol.InboundEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ev, ok := svc.Next(id); ok {
			return ev
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no event for %s", id)
	return protocol.InboundEvent{}
}
