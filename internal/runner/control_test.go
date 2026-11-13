package runner

import (
	"context"
	"testing"

	"github.com/joey/jkp/internal/control"
	"github.com/joey/jkp/internal/protocol"
)

type captureSender struct{ message protocol.OutboundMessage }

func (s *captureSender) Send(msg protocol.OutboundMessage) error {
	s.message = msg
	return nil
}

func TestStartControlForwardsSayThroughConnectedIRCClient(t *testing.T) {
	path := t.TempDir() + "/agent.json"
	sender := &captureSender{}
	cleanup, err := startControl(context.Background(), path, "guest", "e2eg", "e2eh", "ses_guest", "http://127.0.0.1:4200", "", control.Handlers{Role: "guest", Peer: "e2eh", Send: func(to, body string) (string, error) {
		if err := sender.Send(protocol.OutboundMessage{ID: "control", EndpointID: "e2eg", Kind: protocol.KindDM, Target: to, Body: body, InReplyTo: "control"}); err != nil {
			return "", err
		}
		return to, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := control.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Role != "guest" || info.SessionID != "ses_guest" || info.OpenCodeURL != "http://127.0.0.1:4200" {
		t.Fatalf("control binding = %#v", info)
	}
	if target, err := control.NewClient(path).Send(context.Background(), "", "Please review this design"); err != nil || target != "e2eh" {
		t.Fatal(err)
	}
	if sender.message.EndpointID != "e2eg" || sender.message.Target != "e2eh" || sender.message.Body != "Please review this design" {
		t.Fatalf("forwarded message = %#v", sender.message)
	}
	if err := sender.message.Validate(); err != nil {
		t.Fatalf("forwarded outbound message is invalid: %v", err)
	}
}

func TestHostModeStartsControlWithoutPeer(t *testing.T) {
	path := t.TempDir() + "/agent-host.json"
	cleanup, err := startControl(context.Background(), path, "host", "host", "", "ses_host", "", "", control.Handlers{Role: "host"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := control.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Role != "host" || info.Peer != "" {
		t.Fatalf("host control info = %#v", info)
	}
}

func TestStartControlPersistsLogFile(t *testing.T) {
	path := t.TempDir() + "/agent-guest-g1.json"
	want := "/tmp/jkp-guest-g1.log"
	cleanup, err := startControl(context.Background(), path, "guest", "g1", "host", "ses_c1", "", want, control.Handlers{Role: "guest", Peer: "host"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := control.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.LogFile != want {
		t.Fatalf("control LogFile = %q, want %q", info.LogFile, want)
	}
}
