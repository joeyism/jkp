package protocol

import (
	"errors"
	"fmt"
)

// Validation errors returned by Validate. They are exported so callers can
// branch without string matching.
var (
	ErrMissingID         = errors.New("missing id")
	ErrMissingEndpointID = errors.New("missing endpoint_id")
	ErrMissingKind       = errors.New("missing kind")
	ErrMissingTarget     = errors.New("missing target")
	ErrMissingBody       = errors.New("missing body")
	ErrBodyTooLarge      = fmt.Errorf("body exceeds %d bytes", MaxBodyBytes)
	ErrMissingInReplyTo  = errors.New("missing in_reply_to")
	ErrUnknownKind       = errors.New("unknown kind")
)

// Validate reports whether the inbound event is well formed. It does not
// decide whether the event is admitted to a session; that is a policy concern.
func (e InboundEvent) Validate() error {
	if e.ID == "" {
		return ErrMissingID
	}
	if e.EndpointID == "" {
		return ErrMissingEndpointID
	}
	if e.Kind != KindDM && e.Kind != KindRoom {
		if e.Kind == "" {
			return ErrMissingKind
		}
		return ErrUnknownKind
	}
	if e.Target == "" {
		return ErrMissingTarget
	}
	if e.Body == "" {
		return ErrMissingBody
	}
	if len(e.Body) > MaxBodyBytes {
		return ErrBodyTooLarge
	}
	return nil
}

// Validate reports whether the outbound message is well formed and targets an
// inbound event. Routing authorization is enforced by the core router.
func (m OutboundMessage) Validate() error {
	if m.ID == "" {
		return ErrMissingID
	}
	if m.EndpointID == "" {
		return ErrMissingEndpointID
	}
	if m.Kind != KindDM && m.Kind != KindRoom {
		if m.Kind == "" {
			return ErrMissingKind
		}
		return ErrUnknownKind
	}
	if m.Target == "" {
		return ErrMissingTarget
	}
	if m.Body == "" {
		return ErrMissingBody
	}
	if len(m.Body) > MaxBodyBytes {
		return ErrBodyTooLarge
	}
	if m.InReplyTo == "" {
		return ErrMissingInReplyTo
	}
	return nil
}
