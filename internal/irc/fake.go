package irc

import (
	"context"
	"errors"
	"sync"

	"github.com/joey/jkp/internal/protocol"
)

// ErrDisconnected is returned by Send after Disconnect.
var ErrDisconnected = errors.New("irc client disconnected")

// FakeClient is an in-memory Client for tests. It records outbound sends and
// lets tests inject inbound events. It never touches the network.
type FakeClient struct {
	mu        sync.Mutex
	id        string
	nick      string
	connected bool
	events    chan protocol.InboundEvent
	sent      []protocol.OutboundMessage
}

// NewFakeClient returns a fake transport bound to one endpoint. The endpoint
// ID doubles as the endpoint's nick for direct messages.
func FakeClientFor(id string) *FakeClient {
	return &FakeClient{id: id, nick: id, events: make(chan protocol.InboundEvent, 64)}
}

// NewFakeClient is an alias kept for readability in tests.
func NewFakeClient(id string) *FakeClient { return FakeClientFor(id) }

// Connect marks the fake connected.
func (f *FakeClient) Connect(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = true
	return nil
}

// Disconnect closes the events channel exactly once and rejects later sends.
func (f *FakeClient) Disconnect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connected {
		f.connected = false
		close(f.events)
	}
	return nil
}

// Send records one outbound message.
func (f *FakeClient) Send(msg protocol.OutboundMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return ErrDisconnected
	}
	f.sent = append(f.sent, msg)
	return nil
}

// Events delivers inbound events until Disconnect.
func (f *FakeClient) Events() <-chan protocol.InboundEvent { return f.events }

// Sent returns a copy of recorded outbound messages.
func (f *FakeClient) Sent() []protocol.OutboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.OutboundMessage, len(f.sent))
	copy(out, f.sent)
	return out
}

// InjectDM queues an inbound direct message to this endpoint.
func (f *FakeClient) InjectDM(account, nick, body string) {
	f.inject(protocol.InboundEvent{
		ID:         nextFakeID(),
		EndpointID: protocol.EndpointID(f.id),
		Kind:       protocol.KindDM,
		Target:     f.nick,
		Sender:     protocol.Identity{Nick: nick, Account: account},
		Body:       body,
	})
}

// InjectRoom queues an inbound room message, optionally mentioning nicks.
func (f *FakeClient) InjectRoom(channel, account, body string, mentions []string) {
	f.inject(protocol.InboundEvent{
		ID:         nextFakeID(),
		EndpointID: protocol.EndpointID(f.id),
		Kind:       protocol.KindRoom,
		Target:     channel,
		Sender:     protocol.Identity{Account: account},
		Body:       body,
		Mentions:   mentions,
	})
}

func (f *FakeClient) inject(ev protocol.InboundEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return
	}
	select {
	case f.events <- ev:
	default:
		// Test buffer full: drop rather than block the test.
	}
}

var (
	fakeSeqMu sync.Mutex
	fakeSeq   int
)

func nextFakeID() string {
	fakeSeqMu.Lock()
	defer fakeSeqMu.Unlock()
	fakeSeq++
	return "fake-" + itoa(fakeSeq)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
