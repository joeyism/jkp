// Package protocol defines the versioned collaboration contract shared by the
// Go core and every harness adapter. It contains pure data and validation only.
//
// Inbound transport receipt, local admission, model processing, and actual
// reply completion are distinct states; nothing in this package conflates them.
package protocol

// Version is the contract version this core and adapters negotiate on.
const Version = "1.0.0"

// MaxBodyBytes bounds a single inbound or outbound body.
const MaxBodyBytes = 16_384

// SafeSplitBytes leaves room below MaxBodyBytes when an agent splits a reply.
const SafeSplitBytes = 15_000

// Kind classifies a conversation coordinate.
type Kind string

const (
	// KindDM is a direct message to one endpoint.
	KindDM Kind = "dm"
	// KindRoom is an invite-only room conversation.
	KindRoom Kind = "room"
)

// Identity is a remote participant. Account carries authenticated identity
// (for example an IRC SASL account); Nick is a display label only. Nick alone
// must never be treated as proof of ownership or authorization.
type Identity struct {
	Nick    string `json:"nick"`
	Account string `json:"account"`
}

// Authenticated reports whether the identity carries a verified account.
func (i Identity) Authenticated() bool { return i.Account != "" }

// EndpointID names one registered agent endpoint.
type EndpointID string

// Conversation is a conversation coordinate within one endpoint.
type Conversation struct {
	Kind   Kind   `json:"kind"`
	Target string `json:"target"`
}

// InboundEvent is one inbound message delivered to a registered endpoint.
type InboundEvent struct {
	ID         string     `json:"id"`
	EndpointID EndpointID `json:"endpoint_id"`
	Kind       Kind       `json:"kind"`
	Target     string     `json:"target"`
	Sender     Identity   `json:"sender"`
	Body       string     `json:"body"`
	Mentions   []string   `json:"mentions,omitempty"`
	Channel    string     `json:"channel,omitempty"`
	SentAt     int64      `json:"sent_at,omitempty"`
	// HumanInitiated marks a local jkp say rather than an automatic model reply.
	// It starts a new bounded volley at the receiving endpoint.
	HumanInitiated bool `json:"human_initiated,omitempty"`
}

// ReplyTarget is the conversation coordinate a reply must use when answering
// this event. Room traffic is answered in the channel; direct messages are
// answered to the sender. Sender.Nick is preferred because IRC addresses
// nicks, with Account as fallback for account-only identities.
func (e InboundEvent) ReplyTarget() string {
	if e.Kind == KindRoom {
		return e.Target
	}
	if e.Sender.Nick != "" {
		return e.Sender.Nick
	}
	return e.Sender.Account
}

// OutboundMessage is the only authorized outbound path. A reply must name the
// inbound event it answers; raw assistant transcripts are never forwarded.
type OutboundMessage struct {
	ID         string     `json:"id"`
	EndpointID EndpointID `json:"endpoint_id"`
	Kind       Kind       `json:"kind"`
	Target     string     `json:"target"`
	Body       string     `json:"body"`
	InReplyTo  string     `json:"in_reply_to"`
	// HumanInitiated is set only by the local control path for jkp say.
	HumanInitiated bool `json:"human_initiated,omitempty"`
}
