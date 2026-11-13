package runner_test

import (
	"testing"

	"github.com/joey/jkp/internal/protocol"
	"github.com/joey/jkp/internal/runner"
)

type recordClient struct {
	sent []protocol.OutboundMessage
}

func (c *recordClient) Send(msg protocol.OutboundMessage) error {
	c.sent = append(c.sent, msg)
	return nil
}

func (c *recordClient) Events() <-chan protocol.InboundEvent { return nil }
func (c *recordClient) Connect(_ interface{}) error          { return nil }
func (c *recordClient) Disconnect() error                    { return nil }

func TestAnnounceSendsDM(t *testing.T) {
	t.Parallel()
	c := &recordClient{}
	err := runner.Announce(c, "pi", "oc", "hello from pi")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("sent %d, want 1", len(c.sent))
	}
	if c.sent[0].Target != "oc" {
		t.Fatalf("target %q, want oc", c.sent[0].Target)
	}
	if c.sent[0].EndpointID != "pi" {
		t.Fatalf("endpoint %q, want pi", c.sent[0].EndpointID)
	}
	if c.sent[0].Body != "hello from pi" {
		t.Fatalf("body %q", c.sent[0].Body)
	}
	if err := c.sent[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAnnounceSkipsEmpty(t *testing.T) {
	t.Parallel()
	c := &recordClient{}
	if err := runner.Announce(c, "pi", "", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Announce(c, "pi", "oc", ""); err != nil {
		t.Fatal(err)
	}
	if err := runner.Announce(c, "pi", "oc", "   "); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 0 {
		t.Fatalf("sent %d, want 0", len(c.sent))
	}
}
