package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/jkpclient"
	"github.com/joey/jkp/internal/protocol"
)

type failingSender struct{ err error }

func (s *failingSender) Send(protocol.OutboundMessage) error { return s.err }

type sizeCheckingSender struct{ sent testHostSender }

func (s *sizeCheckingSender) Send(msg protocol.OutboundMessage) error {
	if len(msg.Body) > protocol.MaxBodyBytes {
		return protocol.ErrBodyTooLarge
	}
	return s.sent.Send(msg)
}

func (s *sizeCheckingSender) snapshot() []protocol.OutboundMessage { return s.sent.snapshot() }

type deliveryNoticeHarness struct {
	reply   string
	notices []string
}

func (h *deliveryNoticeHarness) Handle(context.Context, jkpclient.Event) (string, error) {
	return h.reply, nil
}

func (h *deliveryNoticeHarness) NotifyError(_ context.Context, notice string) error {
	h.notices = append(h.notices, notice)
	return nil
}

type condenseHarness struct {
	replies []string
	prompts []jkpclient.Event
}

func (h *condenseHarness) Handle(_ context.Context, ev jkpclient.Event) (string, error) {
	h.prompts = append(h.prompts, ev)
	if len(h.replies) == 0 {
		return "", nil
	}
	reply := h.replies[0]
	h.replies = h.replies[1:]
	return reply, nil
}

func TestDeliverReplyNotifiesOpenCodeAboutOversizeWithoutForwarding(t *testing.T) {
	sender := &sizeCheckingSender{}
	body := strings.Repeat("a", protocol.MaxBodyBytes+500)
	h := &deliveryNoticeHarness{reply: body}
	ev := protocol.InboundEvent{
		ID: "evt-oversize", EndpointID: "guest", Kind: protocol.KindDM, Target: "guest",
		Sender: protocol.Identity{Nick: "host"}, Body: "send report",
	}
	var logs []string
	deliverReply(context.Background(), Config{EndpointID: "guest", ControlRole: "guest"}, sender, h, ev, body,
		func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	if got := len(sender.snapshot()); got != 0 {
		t.Fatalf("oversize reply was sent, count=%d", got)
	}
	if len(h.notices) != 1 {
		t.Fatalf("one-way delivery notice count=%d", len(h.notices))
	}
	for _, want := range []string{"[JKP-DELIVERY-ERROR]", "16884 bytes", "16384", "15000", "jkp say", "[JKP-LOCAL-ONLY]", "Start"} {
		if !strings.Contains(h.notices[0], want) {
			t.Errorf("notice %q missing %q", h.notices[0], want)
		}
	}
	_ = logs
}

func TestDeliverReplyCondensesOnceForToolsDisabledHarness(t *testing.T) {
	sender := &sizeCheckingSender{}
	oversize := strings.Repeat("b", protocol.MaxBodyBytes+200)
	h := &condenseHarness{replies: []string{"Condensed summary that fits."}}
	ev := protocol.InboundEvent{
		ID: "evt-condense", EndpointID: "guest", Kind: protocol.KindDM, Target: "guest",
		Sender: protocol.Identity{Nick: "host"}, Body: "send report",
	}
	deliverReply(context.Background(), Config{EndpointID: "guest", ControlRole: "guest"}, sender, h, ev, oversize,
		func(string, ...any) {})
	if len(h.prompts) != 1 || !strings.Contains(h.prompts[0].Body, "15000") {
		t.Fatalf("expected one condense retry, prompts=%#v", h.prompts)
	}
	msgs := sender.snapshot()
	if len(msgs) != 1 || msgs[0].Body != "Condensed summary that fits." {
		t.Fatalf("condensed send = %#v", msgs)
	}
}

func TestDeliverReplyDoesNotRetryTransportFailures(t *testing.T) {
	sender := &failingSender{err: errors.New("connection reset")}
	h := &deliveryNoticeHarness{reply: "answer"}
	ev := protocol.InboundEvent{
		ID: "evt-transport", EndpointID: "guest", Kind: protocol.KindDM, Target: "guest",
		Sender: protocol.Identity{Nick: "host"}, Body: "question",
	}
	deliverReply(context.Background(), Config{EndpointID: "guest"}, sender, h, ev, h.reply,
		func(string, ...any) {})
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "Do NOT retry in a loop") {
		t.Fatalf("transport failure notice = %#v", h.notices)
	}
}

func TestIRCServerRejectionNotifiesOpenCodeOneWay(t *testing.T) {
	h := &deliveryNoticeHarness{}
	var logs []string
	reportIRCServerError(context.Background(), h, "FAIL", "INVALID_UTF8", func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "[JKP-DELIVERY-ERROR]") || !strings.Contains(h.notices[0], "INVALID_UTF8") {
		t.Fatalf("server rejection notice = %#v", h.notices)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "IRC server error") {
		t.Fatalf("server rejection logs = %#v", logs)
	}
}
