//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/irc"
	"github.com/joey/jkp/internal/ergoacct"
	"github.com/joey/jkp/internal/protocol"
)

func TestErgoMulticlientSharesJoinSession(t *testing.T) {
	addr := ergoAddr(t)
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	joinNick, senderNick := "j"+suffix, "s"+suffix
	joinPass, senderPass := "join-"+suffix, "sender-"+suffix
	registerTestAccount(t, addr, joinNick, joinPass)
	registerTestAccount(t, addr, senderNick, senderPass)

	newClient := func() (*irc.RealClient, error) {
		c := irc.NewRealClient(irc.RealConfig{Host: host, Port: port, Nick: joinNick, Account: joinNick, Password: joinPass})
		if err := c.Connect(context.Background()); err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = c.Disconnect() })
		return c, nil
	}
	connected := make(chan *irc.RealClient, 2)
	connectErrors := make(chan error, 2)
	for range 2 {
		go func() {
			client, err := newClient()
			if err != nil {
				connectErrors <- err
				return
			}
			connected <- client
		}()
	}
	var clients []*irc.RealClient
	for range 2 {
		select {
		case client := <-connected:
			clients = append(clients, client)
		case err := <-connectErrors:
			t.Fatalf("concurrent connect as %s: %v", joinNick, err)
		case <-time.After(12 * time.Second):
			t.Fatal("two concurrent SASL joins did not connect")
		}
	}
	first, second := clients[0], clients[1]
	sender := irc.NewRealClient(irc.RealConfig{Host: host, Port: port, Nick: senderNick, Account: senderNick, Password: senderPass})
	if err := sender.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Disconnect() }()
	time.Sleep(200 * time.Millisecond)
	if err := sender.Send(protocol.OutboundMessage{ID: "multiclient-probe", EndpointID: protocol.EndpointID(senderNick), Kind: protocol.KindDM, Target: joinNick, Body: "grant probe", InReplyTo: "multiclient-probe"}); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]*irc.RealClient{"first": first, "second": second} {
		select {
		case ev := <-client.Events():
			if ev.Body != "grant probe" || ev.Sender.Nick != senderNick {
				t.Fatalf("%s session received unexpected event: %+v", name, ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s multiclient connection did not receive DM to shared join nick", name)
		}
	}
	clientsToSend := []*irc.RealClient{first, second}
	sendErrs := make(chan error, len(clientsToSend))
	for i, client := range clientsToSend {
		go func(i int, client *irc.RealClient) {
			body := fmt.Sprintf("outbound-%d", i)
			sendErrs <- client.Send(protocol.OutboundMessage{ID: body, EndpointID: protocol.EndpointID(joinNick), Kind: protocol.KindDM, Target: senderNick, Body: body, InReplyTo: body})
		}(i, client)
	}
	for range len(clientsToSend) {
		if err := <-sendErrs; err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case ev := <-sender.Events():
			seen[ev.Body] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("shared join connections did not both deliver outbound requests; seen=%v", seen)
		}
	}
	if !seen["outbound-0"] || !seen["outbound-1"] {
		t.Fatalf("multiclient outbound messages = %v", seen)
	}
}

func TestErgoRegisterNewNeverChangesExistingGuestPassword(t *testing.T) {
	addr := ergoAddr(t)
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	nick := freshNick("g")
	oldPassword := "old-" + nick
	registerTestAccount(t, addr, nick, oldPassword)
	registrar := ergoacct.Registrar{Addr: addr, OperUser: operUser, OperPass: operPass}
	if err := registrar.RegisterNew(nick, "attacker-selected-password"); err == nil {
		t.Fatal("RegisterNew succeeded for an existing account")
	}
	client := irc.NewRealClient(irc.RealConfig{Host: host, Port: port, Nick: nick, Account: nick, Password: oldPassword})
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("existing password was changed by RegisterNew: %v", err)
	}
	_ = client.Disconnect()
}

func TestErgoMonitorReportsDMPeerOffline(t *testing.T) {
	addr := ergoAddr(t)
	hostNick, guestNick := freshNick("h"), freshNick("g")
	guestPass := "guest-" + guestNick
	registerTestAccount(t, addr, guestNick, guestPass)

	host := dialRaw(t, addr, "m"+hostNick)
	t.Cleanup(host.close)
	host.send("MONITOR + " + guestNick)

	guest := irc.NewRealClient(irc.RealConfig{Host: "127.0.0.1", Port: 6667, Nick: guestNick, Account: guestNick, Password: guestPass})
	if err := guest.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if line := host.waitContains(" 730 ", 5*time.Second); !strings.Contains(line, "730") || !strings.Contains(strings.ToLower(line), strings.ToLower(guestNick)) {
		t.Fatalf("MONITOR did not report guest online: %q", line)
	}
	_ = guest.Disconnect()
	if line := host.waitContains(" 731 ", 5*time.Second); !strings.Contains(line, "731") || !strings.Contains(strings.ToLower(line), strings.ToLower(guestNick)) {
		t.Fatalf("MONITOR did not report guest offline: %q", line)
	}
}

func TestErgoNoQuitForDMOnlyPeer(t *testing.T) {
	addr := ergoAddr(t)
	guestNick := freshNick("g")
	guestPass := "guest-" + guestNick
	registerTestAccount(t, addr, guestNick, guestPass)

	host := dialRaw(t, addr, "m"+freshNick("h"))
	t.Cleanup(host.close)
	guest := irc.NewRealClient(irc.RealConfig{Host: "127.0.0.1", Port: 6667, Nick: guestNick, Account: guestNick, Password: guestPass})
	if err := guest.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = guest.Disconnect()

	if line := host.waitContains(" QUIT ", 750*time.Millisecond); strings.Contains(line, " QUIT ") {
		t.Fatalf("DM-only peer departure unexpectedly sent QUIT to host: %q", line)
	}
}

func freshNick(prefix string) string {
	return prefix + fmt.Sprintf("%x", time.Now().UnixNano())
}
