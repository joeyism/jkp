// Package irc owns IRC transport behind a small interface so the core can be
// tested without a server and the real client can be swapped independently.
package irc

import (
	"context"

	"github.com/joey/jkp/internal/protocol"
)

// Client is the transport contract. Events delivers inbound messages; Send
// emits one explicit outbound message. A closed Events channel means the
// connection is gone, not that the review is finished.
type Client interface {
	Connect(ctx context.Context) error
	Disconnect() error
	Send(protocol.OutboundMessage) error
	Events() <-chan protocol.InboundEvent
}
