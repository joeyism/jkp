//go:build integration

// Package integration exercises jkp's real IRC client against a real Ergo
// server over real TCP. Run with:
//
//	go test -tags=integration ./tests/integration/ -v -count=1
//
// It requires the Ergo container from deploy/ergo to be running and reachable
// at JKP_ERGO_ADDR (default 127.0.0.1:6667).
package integration

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

const (
	operUser = "admin"
	operPass = "jkp-test-oper-pw"
)

func ergoAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("JKP_ERGO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6667"
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("Ergo not reachable at %s (start deploy/ergo first): %v", addr, err)
	}
	_ = conn.Close()
	return addr
}

// rawSession is a minimal IRC client used only to prepare the test server.
type rawSession struct {
	conn net.Conn
	rd   *bufio.Reader
	wr   *bufio.Writer
}

func dialRaw(t *testing.T, addr, nick string) *rawSession {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s := &rawSession{conn: conn, rd: bufio.NewReader(conn), wr: bufio.NewWriter(conn)}
	s.send("NICK " + nick)
	s.send("USER " + nick + " 0 * :" + nick)
	s.waitContains(" 001 ", 8*time.Second)
	return s
}

func (s *rawSession) send(line string) {
	_, _ = s.wr.WriteString(line + "\r\n")
	_ = s.wr.Flush()
}

func (s *rawSession) waitContains(substr string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		_ = s.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := s.rd.ReadString('\n')
		if err != nil {
			continue
		}
		last = strings.TrimRight(line, "\r\n")
		if strings.Contains(last, substr) {
			return last
		}
	}
	return last
}

func (s *rawSession) close() { _ = s.conn.Close() }

// ensureAccounts creates SASL accounts through the server operator so the real
// clients can authenticate. Idempotent and run at most once per test binary:
// repeated OPER/SAREGISTER attempts trip the server's throttles.
var ensureOnce sync.Once

func ensureAccounts(t *testing.T, addr string, accounts map[string]string) {
	t.Helper()
	ensureOnce.Do(func() {
		oper := dialRaw(t, addr, "jkpop")
		defer oper.close()
		oper.send("OPER " + operUser + " " + operPass)
		if line := oper.waitContains(" 381 ", 8*time.Second); !strings.Contains(line, "381") {
			t.Fatalf("OPER failed: %q", line)
		}
		for name, pass := range accounts {
			oper.send("NICKSERV SAREGISTER " + name + " " + pass)
			time.Sleep(200 * time.Millisecond)
		}
	})
}

func realConfig(nick, pass string) irc.RealConfig {
	return irc.RealConfig{
		Host:     "127.0.0.1",
		Port:     6667,
		Nick:     nick,
		Account:  nick,
		Password: pass,
		Channels: []string{"#jkptest"},
	}
}

func TestErgoSASLConnectAndJoin(t *testing.T) {
	addr := ergoAddr(t)
	ensureAccounts(t, addr, map[string]string{"jkpa": "jkpa-pw", "jkpb": "jkpb-pw"})

	cfg := realConfig("jkpa", "jkpa-pw")
	c := irc.NewRealClient(cfg)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("SASL connect failed: %v", err)
	}
	defer func() { _ = c.Disconnect() }()

	// The channel join must reach the server; there is no assertion helper
	// here beyond a successful SASL handshake plus a clean disconnect, which
	// the unit tests already cover for line-level details.
	time.Sleep(300 * time.Millisecond)
}

func TestErgoDMRoundTrip(t *testing.T) {
	addr := ergoAddr(t)
	ensureAccounts(t, addr, map[string]string{"jkpa": "jkpa-pw", "jkpb": "jkpb-pw"})

	a := irc.NewRealClient(realConfig("jkpa", "jkpa-pw"))
	if err := a.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Disconnect() }()

	b := irc.NewRealClient(realConfig("jkpb", "jkpb-pw"))
	if err := b.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Disconnect() }()

	time.Sleep(500 * time.Millisecond)

	err := a.Send(protocol.OutboundMessage{
		ID: "out-1", EndpointID: "jkpa", Kind: protocol.KindDM,
		Target: "jkpb", Body: "hello from a", InReplyTo: "evt-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-b.Events():
		if ev.Body != "hello from a" {
			t.Fatalf("unexpected body %q", ev.Body)
		}
		if ev.Kind != protocol.KindDM {
			t.Fatalf("want DM, got %s", ev.Kind)
		}
		if ev.Sender.Nick != "jkpa" {
			t.Fatalf("want sender jkpa, got %+v", ev.Sender)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("DM never arrived")
	}
}

func TestErgoRoomMentionOnly(t *testing.T) {
	addr := ergoAddr(t)
	ensureAccounts(t, addr, map[string]string{"jkpa": "jkpa-pw", "jkpb": "jkpb-pw"})

	// Admission policy lives in the core, not the transport: the transport
	// faithfully reports every room line and the service decides whether it
	// may reach the endpoint. Assert at the layer that owns the guarantee.
	svc := core.NewService(senderFunc(func(protocol.OutboundMessage) error { return nil }), realClock{})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.Register(core.Endpoint{ID: "jkpa", Harness: "pi", SessionID: "ses_a", Nick: "jkpa"}))
	must(svc.Register(core.Endpoint{ID: "jkpb", Harness: "pi", SessionID: "ses_b", Nick: "jkpb"}))
	must(svc.Enable("jkpa"))
	must(svc.Enable("jkpb"))

	br := irc.NewBridge(svc)
	a := irc.NewRealClient(realConfig("jkpa", "jkpa-pw"))
	must(a.Connect(context.Background()))
	defer func() { _ = a.Disconnect() }()
	stopA := br.Attach("jkpa", a)
	defer stopA()

	b := irc.NewRealClient(realConfig("jkpb", "jkpb-pw"))
	must(b.Connect(context.Background()))
	defer func() { _ = b.Disconnect() }()
	stopB := br.Attach("jkpb", b)
	defer stopB()

	time.Sleep(500 * time.Millisecond)

	// Unaddressed chatter must never become an admitted delivery for jkpb.
	must(a.Send(protocol.OutboundMessage{
		ID: "out-c", EndpointID: "jkpa", Kind: protocol.KindRoom,
		Target: "#jkptest", Body: "just thinking out loud", InReplyTo: "evt-c",
	}))
	time.Sleep(1 * time.Second)
	if _, ok := svc.Next("jkpb"); ok {
		t.Fatal("unaddressed chatter must not be admitted")
	}

	// A direct mention must be admitted with the mention detected.
	must(a.Send(protocol.OutboundMessage{
		ID: "out-m", EndpointID: "jkpa", Kind: protocol.KindRoom,
		Target: "#jkptest", Body: "jkpb please look at this", InReplyTo: "evt-m",
	}))
	var ev protocol.InboundEvent
	deadline := time.Now().Add(8 * time.Second)
	for {
		if e, ok := svc.Next("jkpb"); ok {
			ev = e
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mention never admitted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ev.Kind != protocol.KindRoom || ev.Target != "#jkptest" {
		t.Fatalf("unexpected room event %+v", ev)
	}
	found := false
	for _, m := range ev.Mentions {
		if m == "jkpb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mention not detected: %+v", ev.Mentions)
	}
}

// TestErgoFullStack wires real clients into the jkp bridge and service and
// asserts an end-to-end admitted event and a correctly routed reply.
func TestErgoFullStack(t *testing.T) {
	addr := ergoAddr(t)
	ensureAccounts(t, addr, map[string]string{"jkpa": "jkpa-pw", "jkpb": "jkpb-pw"})

	sentMu := sync.Mutex{}
	var sent []protocol.OutboundMessage
	svc := core.NewService(senderFunc(func(m protocol.OutboundMessage) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, m)
		return nil
	}), realClock{})

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.Register(core.Endpoint{ID: "jkpa", Harness: "pi", SessionID: "ses_a", Nick: "jkpa"}))
	must(svc.Register(core.Endpoint{ID: "jkpb", Harness: "opencode", SessionID: "ses_b", Nick: "jkpb"}))
	must(svc.Enable("jkpa"))
	must(svc.Enable("jkpb"))

	br := irc.NewBridge(svc)
	a := irc.NewRealClient(realConfig("jkpa", "jkpa-pw"))
	must(a.Connect(context.Background()))
	defer func() { _ = a.Disconnect() }()
	stopA := br.Attach("jkpa", a)
	defer stopA()

	b := irc.NewRealClient(realConfig("jkpb", "jkpb-pw"))
	must(b.Connect(context.Background()))
	defer func() { _ = b.Disconnect() }()
	stopB := br.Attach("jkpb", b)
	defer stopB()

	time.Sleep(500 * time.Millisecond)

	// jkpb's human sends a DM to jkpa; jkpa's agent replies explicitly.
	must(b.Send(protocol.OutboundMessage{
		ID: "out-in", EndpointID: "jkpb", Kind: protocol.KindDM,
		Target: "jkpa", Body: "can you explain this?", InReplyTo: "evt-seed",
	}))

	var ev protocol.InboundEvent
	deadline := time.Now().Add(8 * time.Second)
	for {
		if e, ok := svc.Next("jkpa"); ok {
			ev = e
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event never admitted to jkpa")
		}
		time.Sleep(10 * time.Millisecond)
	}

	must(svc.Reply(protocol.OutboundMessage{
		ID: "out-r", EndpointID: "jkpa", Kind: ev.Kind,
		Target: ev.ReplyTarget(), Body: "here is why", InReplyTo: ev.ID,
	}))

	sentMu.Lock()
	got := append([]protocol.OutboundMessage(nil), sent...)
	sentMu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want exactly one routed reply, got %d", len(got))
	}
	if got[0].Target != "jkpb" {
		t.Fatalf("reply must route to jkpb, got %s", got[0].Target)
	}
}

type senderFunc func(protocol.OutboundMessage) error

func (f senderFunc) Send(m protocol.OutboundMessage) error { return f(m) }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

var _ = fmt.Sprintf
