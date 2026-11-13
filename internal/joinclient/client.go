// Package joinclient performs the authenticated bootstrap exchange that turns
// a shared room invite into one guest's private IRC identity.
package joinclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/joinproto"
	"github.com/joey/jkp/internal/protocol"
)

const grantTimeout = 15 * time.Second

type Client interface {
	Connect(context.Context) error
	Send(protocol.OutboundMessage) error
	Events() <-chan protocol.InboundEvent
	Disconnect() error
}

// RequestGrant opens one TLS/SASL bootstrap connection, requests a guest
// identity, accepts only a host grant sealed to this request, then disconnects.
func RequestGrant(ctx context.Context, newClient func(irc.RealConfig) Client, inv invite.Invite) (string, string, error) {
	return requestGrant(ctx, newClient, inv, grantTimeout)
}

func requestGrant(ctx context.Context, newClient func(irc.RealConfig) Client, inv invite.Invite, timeout time.Duration) (string, string, error) {
	if newClient == nil {
		return "", "", fmt.Errorf("join IRC client factory is required")
	}
	request, private, err := joinproto.NewRequest()
	if err != nil {
		return "", "", fmt.Errorf("create secure join request: %w", err)
	}
	client := newClient(irc.RealConfig{
		Host: inv.Host, Port: inv.Port, TLS: true, CertFP: inv.FP,
		Nick: inv.JoinNick, Account: inv.JoinNick, Password: inv.JoinPassword,
	})
	if client == nil {
		return "", "", fmt.Errorf("join IRC client factory returned nil")
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Disconnect()
		return "", "", fmt.Errorf("connect to shared join account: %w", err)
	}
	defer func() { _ = client.Disconnect() }()
	messageID := "join-request-" + request.Nonce
	if err := client.Send(protocol.OutboundMessage{
		ID: messageID, EndpointID: protocol.EndpointID(inv.JoinNick), Kind: protocol.KindDM,
		Target: inv.Peer, Body: joinproto.FormatJoin(request), InReplyTo: messageID,
	}); err != nil {
		return "", "", fmt.Errorf("send guest join request: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return "", "", fmt.Errorf("host agent did not answer; on the host, run /jkp (or jkp host) and try again")
			}
			return "", "", waitCtx.Err()
		case ev, ok := <-client.Events():
			if !ok {
				return "", "", fmt.Errorf("host agent disconnected before granting a guest identity")
			}
			if ev.Kind != protocol.KindDM || ev.Sender.Nick != inv.Peer {
				continue
			}
			if deny, err := joinproto.ParseDeny(ev.Body); err == nil {
				if deny.Nonce == request.Nonce {
					return "", "", fmt.Errorf("host could not add this guest: %s", deny.Reason)
				}
				continue
			}
			if _, err := joinproto.ParseGrant(ev.Body); err != nil {
				continue
			}
			grant, err := joinproto.OpenGrant(ev.Body, request.Nonce, private)
			if errors.Is(err, joinproto.ErrNotMine) {
				continue
			}
			if err != nil {
				return "", "", fmt.Errorf("invalid sealed guest grant: %w", err)
			}
			return grant.Nick, grant.Password, nil
		}
	}
}
