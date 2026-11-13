package irc_test

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/protocol"
)

// stubServer speaks a minimal IRC subset over a pipe so the real client can be
// tested without a network or an Ergo deployment.
type stubServer struct {
	conn net.Conn
	rd   *bufio.Reader

	mu       sync.Mutex
	received []string
	saslDone bool
}

func newStub(t *testing.T) (client net.Conn, s *stubServer) {
	t.Helper()
	a, b := net.Pipe()
	s = &stubServer{conn: b, rd: bufio.NewReader(b)}
	go s.serve(t)
	return a, s
}

func (s *stubServer) serve(t *testing.T) {
	defer func() { _ = s.conn.Close() }()
	for {
		line, err := s.rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.mu.Lock()
		s.received = append(s.received, line)
		s.mu.Unlock()

		switch {
		case strings.HasPrefix(line, "CAP LS"):
			s.write(":stub CAP * LS :sasl")
		case strings.HasPrefix(line, "CAP REQ"):
			s.write(":stub CAP * ACK :sasl")
		case line == "AUTHENTICATE PLAIN":
			s.write("AUTHENTICATE +")
		case strings.HasPrefix(line, "AUTHENTICATE ") && line != "AUTHENTICATE *":
			s.write(":stub 903 agent :SASL authentication successful")
			s.mu.Lock()
			s.saslDone = true
			s.mu.Unlock()
		case strings.HasPrefix(line, "NICK"):
			// wait for USER before welcoming
		case strings.HasPrefix(line, "USER"):
			s.write(":stub 001 agent :Welcome to the stub")
		case strings.HasPrefix(line, "PING"):
			payload := strings.TrimPrefix(line, "PING")
			s.write(":stub PONG" + payload)
		case strings.HasPrefix(line, "QUIT"):
			return
		}
	}
}

func (s *stubServer) write(line string) {
	_, _ = s.conn.Write([]byte(line + "\r\n"))
}

func (s *stubServer) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.received))
	copy(out, s.received)
	return out
}

func newRealClient(t *testing.T, dial func() (net.Conn, error)) *irc.RealClient {
	t.Helper()
	cfg := irc.RealConfig{
		Nick:     "agent",
		Account:  "agent",
		Password: "secret",
		Channels: []string{"#lab"},
	}
	return irc.NewRealClient(cfg).WithDial(dial)
}

func TestRealConnectAndSASL(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })

	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()

	if !stub.saslDone {
		t.Fatalf("SASL did not complete; got lines %v", stub.lines())
	}
	// JOIN is written during handshake but recorded asynchronously by the
	// stub, so poll rather than asserting on a single snapshot.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range stub.lines() {
			if strings.HasPrefix(l, "JOIN #lab") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("client must join configured channels, got %v", stub.lines())
}

func TestRealInboundDMEmitsEvent(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()

	stub.write(":alice!alice@host PRIVMSG agent :hello there")

	select {
	case ev := <-c.Events():
		if ev.Kind != protocol.KindDM || ev.Sender.Nick != "alice" || ev.Body != "hello there" {
			t.Fatalf("unexpected DM event %+v", ev)
		}
		if err := ev.Validate(); err != nil {
			t.Fatalf("event must validate: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no DM event delivered")
	}
}

func TestRealInboundRoomEmitsEvent(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()

	stub.write(":bob!bob@host PRIVMSG #lab :agent please look")

	select {
	case ev := <-c.Events():
		if ev.Kind != protocol.KindRoom || ev.Target != "#lab" {
			t.Fatalf("unexpected room event %+v", ev)
		}
		if len(ev.Mentions) != 1 || ev.Mentions[0] != "agent" {
			t.Fatalf("mentions not detected: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no room event delivered")
	}
}

func TestRealSendWritesPrivmsg(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()

	err := c.Send(protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent", Kind: protocol.KindDM,
		Target: "alice", Body: "pong", InReplyTo: "evt-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range stub.lines() {
			if strings.HasPrefix(l, "PRIVMSG alice :pong") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never saw PRIVMSG alice :pong; got %v", stub.lines())
}

func TestRealSendSplitsLongPrivmsg(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	body := strings.Repeat("abcdefghij", 80)
	if err := c.Send(protocol.OutboundMessage{
		ID: "out-long", EndpointID: "agent", Kind: protocol.KindDM,
		Target: "alice", Body: body, InReplyTo: "evt-1",
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var priv []string
	for time.Now().Before(deadline) {
		priv = nil
		for _, l := range stub.lines() {
			if strings.HasPrefix(l, "PRIVMSG alice :") {
				priv = append(priv, strings.TrimPrefix(l, "PRIVMSG alice :"))
			}
		}
		if len(priv) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(priv) < 2 {
		t.Fatalf("want chunked PRIVMSG, got %v", stub.lines())
	}
	for _, part := range priv {
		if !strings.HasPrefix(part, "jkp+ ") {
			t.Fatalf("unframed chunk %q", part)
		}
	}
}

func TestMonitorWritesCommand(t *testing.T) {
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	if err := c.Monitor("g1a2b3c4"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, line := range stub.lines() {
			if line == "MONITOR + g1a2b3c4" {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("MONITOR command not written: %v", stub.lines())
}

func TestPresenceOnlineFrom730(t *testing.T) {
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	stub.write(":stub 730 agent :g1a2b3c4!user@host")
	select {
	case ev := <-c.Presence():
		if ev.Nick != "g1a2b3c4" || !ev.Online {
			t.Fatalf("presence event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no online presence event")
	}
}

func TestPresenceOfflineFrom731(t *testing.T) {
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	stub.write(":stub 731 agent :g1a2b3c4!user@host,g2b3c4d5!user@host")
	for _, want := range []string{"g1a2b3c4", "g2b3c4d5"} {
		select {
		case ev := <-c.Presence():
			if ev.Nick != want || ev.Online {
				t.Fatalf("presence event = %#v, want offline %s", ev, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("no offline presence event for %s", want)
		}
	}
}

func TestPresenceChannelClosedOnDisconnect(t *testing.T) {
	conn, _ := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-c.Presence():
		if ok {
			t.Fatal("presence channel still open after disconnect")
		}
	case <-time.After(time.Second):
		t.Fatal("presence channel did not close")
	}
}

func TestRealInboundChunksReassembleToOneEvent(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	stub.write(":alice!alice@host PRIVMSG agent :jkp+ 1/2 hello ")
	select {
	case <-c.Events():
		t.Fatal("incomplete chunk should not emit")
	case <-time.After(50 * time.Millisecond):
	}
	stub.write(":alice!alice@host PRIVMSG agent :jkp+ 2/2 world")
	select {
	case ev := <-c.Events():
		if ev.Body != "hello world" {
			t.Fatalf("body = %q", ev.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reassembled event")
	}
}

func TestRealClientMarksAndRemovesHumanSayPrefix(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	if err := c.Send(protocol.OutboundMessage{
		ID: "out-human", EndpointID: "agent", Kind: protocol.KindDM,
		Target: "alice", Body: "start a volley", InReplyTo: "out-human", HumanInitiated: true,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	found := false
	for time.Now().Before(deadline) {
		for _, line := range stub.lines() {
			if strings.Contains(line, "PRIVMSG alice :[jkp-human-say] start a volley") {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("outbound manual jkp say was not marked")
	}

	stub.write(":alice!alice@host PRIVMSG agent :[jkp-human-say] Start a new review volley")
	select {
	case ev := <-c.Events():
		if !ev.HumanInitiated || ev.Body != "Start a new review volley" {
			t.Fatalf("manual say event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no human-initiated event received")
	}
}

func TestRealRepliesToPing(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()

	stub.write("PING :keepalive")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range stub.lines() {
			if strings.HasPrefix(l, "PONG") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("client must answer PING; got %v", stub.lines())
}

func TestRealDisconnectClosesEvents(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	_ = stub
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-c.Events():
		if open {
			t.Fatal("Events must close after Disconnect")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Events never closed")
	}
}

func TestRealSendAfterDisconnectFails(t *testing.T) {
	t.Parallel()
	conn, _ := newStub(t)
	c := newRealClient(t, func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Disconnect()
	err := c.Send(protocol.OutboundMessage{
		ID: "out-1", EndpointID: "agent", Kind: protocol.KindDM,
		Target: "alice", Body: "x", InReplyTo: "e",
	})
	if err == nil {
		t.Fatal("send after disconnect must fail")
	}
}

func TestRealClientReportsServerFailAndErrorNumerics(t *testing.T) {
	t.Parallel()
	conn, stub := newStub(t)
	var mu sync.Mutex
	var seen []string
	c := irc.NewRealClient(irc.RealConfig{
		Nick:     "agent",
		Account:  "agent",
		Password: "secret",
		OnServerError: func(command, detail string) {
			mu.Lock()
			seen = append(seen, command+":"+detail)
			mu.Unlock()
		},
	}).WithDial(func() (net.Conn, error) { return conn, nil })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()

	stub.write(":stub FAIL PRIVMSG INVALID_UTF8 :Message rejected for containing invalid UTF-8")
	stub.write(":stub 401 agent ghost :No such nick/channel")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(seen)
		mu.Unlock()
		if count == 2 {
			mu.Lock()
			defer mu.Unlock()
			if !strings.Contains(seen[0], "INVALID_UTF8") || !strings.Contains(seen[1], "No such nick/channel") {
				t.Fatalf("unexpected server error details: %v", seen)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("expected FAIL and 401 to be reported, got %v", seen)
}
