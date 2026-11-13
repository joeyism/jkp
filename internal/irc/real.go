package irc

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joey/jkp/internal/protocol"
)

const humanSayPrefix = "[jkp-human-say] "

// RealConfig configures one authenticated IRC identity for an agent endpoint.
type RealConfig struct {
	Host     string
	Port     int
	Nick     string
	Account  string // SASL authcid
	Password string // SASL passphrase
	Channels []string
	// TLS enables transport security. Required for cross-machine use.
	TLS bool
	// CertFP is the hex SHA-256 of the server leaf certificate, used to pin
	// a self-signed test server. Required for TLS. Empty means "verify with
	// the system roots", which rejects self-signed servers.
	CertFP string
	// TLSConfig customises TLS verification. When nil and CertFP is empty,
	// the system root pool is used. When CertFP is set, CertFP takes priority.
	TLSConfig *tls.Config
	// OnServerError reports IRC FAIL messages and error numerics (4xx/5xx).
	OnServerError func(command, detail string)
}

// RealClient is a minimal IRC client behind the Client interface. It handles
// exactly the protocol subset this product needs: CAP/SASL PLAIN negotiation,
// registration, channel joins, PRIVMSG send/receive, PING keepalive, and QUIT.
// Anything outside that subset is ignored rather than reimplemented.
type RealClient struct {
	cfg  RealConfig
	dial func() (net.Conn, error)

	mu     sync.Mutex
	conn   net.Conn
	writer *bufio.Writer

	events   chan protocol.InboundEvent
	presence chan PresenceEvent
	chunks   map[string]*chunkAssembler

	closeOnce sync.Once
	done      chan struct{}
}

// NewRealClient returns a client for cfg. Call WithDial to inject a connection
// in tests; production uses net.Dial with optional TLS.
func NewRealClient(cfg RealConfig) *RealClient {
	return &RealClient{
		cfg:      cfg,
		events:   make(chan protocol.InboundEvent, 64),
		presence: make(chan PresenceEvent, 64),
		chunks:   make(map[string]*chunkAssembler),
		done:     make(chan struct{}),
	}
}

// WithDial overrides connection establishment. Intended for tests.
func (c *RealClient) WithDial(d func() (net.Conn, error)) *RealClient {
	c.dial = d
	return c
}

// Connect dials, negotiates SASL, registers the nick, and joins channels.
func (c *RealClient) Connect(ctx context.Context) error {
	dial := c.dial
	if dial == nil {
		dial = c.defaultDial
	}
	conn, err := dial()
	if err != nil {
		return fmt.Errorf("dial irc: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.writer = bufio.NewWriter(conn)
	c.mu.Unlock()

	// The handshake and the read loop must share one buffered reader,
	// otherwise bytes buffered during negotiation are lost.
	rd := bufio.NewReader(conn)
	// Bound the handshake so a silent or throttling server cannot hang the
	// client forever. The deadline is cleared before the read loop takes over.
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := c.handshake(rd); err != nil {
		_ = conn.Close()
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	go c.readLoop(conn, rd)
	return nil
}

// handshakeTimeout bounds SASL negotiation and registration. Server-side
// connection or login throttling shows up here as a clean error rather than a
// hung goroutine.
const handshakeTimeout = 10 * time.Second

func (c *RealClient) defaultDial() (net.Conn, error) {
	host := c.cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.cfg.Port
	if port == 0 {
		if c.cfg.TLS {
			port = 6697
		} else {
			port = 6667
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if !c.cfg.TLS {
		return conn, nil
	}
	// Cross-machine traffic must be encrypted. When CertFP is set, pin the
	// leaf certificate. Otherwise fall back to TLSConfig or system roots.
	tlsCfg := c.cfg.TLSConfig
	if tlsCfg == nil {
		if c.cfg.CertFP != "" {
			fp, err := hex.DecodeString(c.cfg.CertFP)
			if err != nil || len(fp) != sha256.Size {
				_ = conn.Close()
				return nil, fmt.Errorf("certificate fingerprint: want 32 bytes")
			}
			tlsCfg = pinnedConfig(fp)
		} else {
			tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
	}
	tc := tls.Client(conn, tlsCfg)
	if err := tc.Handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return tc, nil
}

// pinnedConfig returns a tls.Config that accepts a self-signed server only
// when the SHA-256 of the leaf DER matches fp.
func pinnedConfig(fp []byte) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // pin replaces root verification
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no peer certificate")
			}
			sum := sha256.Sum256(raw[0])
			if subtle.ConstantTimeCompare(sum[:], fp) != 1 {
				return errors.New("certificate fingerprint mismatch")
			}
			return nil
		},
	}
}

// DialTLSPinned opens a TLS connection to addr and verifies the server leaf
// certificate SHA-256 matches fpHex (64 hex chars). It returns nil on success
// and an error containing "fingerprint" on mismatch. Used by jkp join to
// verify reachability before spawning the agent.
func DialTLSPinned(addr, fpHex string) error {
	fp, err := hex.DecodeString(fpHex)
	if err != nil || len(fp) != sha256.Size {
		return fmt.Errorf("certificate fingerprint: want 32 bytes")
	}
	conn, err := tls.Dial("tcp", addr, pinnedConfig(fp))
	if err != nil {
		return err
	}
	return conn.Close()
}

// FingerprintPEM extracts the SHA-256 hex fingerprint of the first CERTIFICATE
// PEM block. Used by jkp host to compute the invite fingerprint.
func FingerprintPEM(pemData []byte) (string, error) {
	for {
		var blk *pem.Block
		blk, pemData = pem.Decode(pemData)
		if blk == nil {
			return "", fmt.Errorf("no CERTIFICATE block in PEM data")
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		sum := sha256.Sum256(blk.Bytes)
		return hex.EncodeToString(sum[:]), nil
	}
}

// handshake performs CAP/SASL negotiation and registration synchronously.
func (c *RealClient) handshake(rd *bufio.Reader) error {
	send := func(line string) error { return c.write(line) }

	if err := send("CAP LS 302"); err != nil {
		return err
	}
	// CAP LS may span multiple lines. Read until we see the line that lists
	// sasl, which is the last one.
	if _, err := readUntilContains(rd, "CAP", "sasl"); err != nil {
		return err
	}
	if err := send("CAP REQ :sasl"); err != nil {
		return err
	}
	if _, err := readUntilContains(rd, "CAP", "ACK"); err != nil {
		return err
	}

	// SASL PLAIN: empty authzid, then authcid, then passwd.
	if err := send("AUTHENTICATE PLAIN"); err != nil {
		return err
	}
	if _, err := readUntil(rd, "AUTHENTICATE"); err != nil {
		return err
	}
	cred := base64.StdEncoding.EncodeToString(
		[]byte("\x00" + c.cfg.Account + "\x00" + c.cfg.Password))
	if err := send("AUTHENTICATE " + cred); err != nil {
		return err
	}
	if _, err := readUntil(rd, "903"); err != nil {
		return fmt.Errorf("sasl failed: %w", err)
	}
	if err := send("CAP END"); err != nil {
		return err
	}

	if err := send("NICK " + c.cfg.Nick); err != nil {
		return err
	}
	if err := send("USER " + c.cfg.Account + " 0 * :jkp agent"); err != nil {
		return err
	}
	if _, err := readUntil(rd, "001"); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	for _, ch := range c.cfg.Channels {
		if err := send("JOIN " + ch); err != nil {
			return err
		}
	}
	return nil
}

// readUntil consumes lines until one carries the given IRC command/numeric.
func readUntil(rd *bufio.Reader, prefix string) (string, error) {
	for i := 0; i < 64; i++ {
		line, err := rd.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		msg := parseLine(line)
		if msg.command == prefix || strings.HasPrefix(msg.command, prefix) {
			return line, nil
		}
	}
	return "", errors.New("handshake timeout waiting for " + prefix)
}

// readUntilContains consumes lines until one has the given command and
// contains the substring in its full text. This handles multi-line CAP
// responses where several lines share the same command.
func readUntilContains(rd *bufio.Reader, command, substr string) (string, error) {
	for i := 0; i < 64; i++ {
		line, err := rd.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("waiting for %s containing %s: %w", command, substr, err)
		}
		line = strings.TrimRight(line, "\r\n")
		msg := parseLine(line)
		if (msg.command == command || strings.HasPrefix(msg.command, command)) &&
			strings.Contains(line, substr) {
			return line, nil
		}
	}
	return "", errors.New("handshake timeout waiting for " + command + " containing " + substr)
}

// Disconnect quits and closes the transport. Events is closed afterwards.
func (c *RealClient) Disconnect() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		conn := c.conn
		if conn != nil {
			_, _ = c.writer.WriteString("QUIT :bye\r\n")
			_ = c.writer.Flush()
			err = conn.Close()
		}
		c.mu.Unlock()
	})
	return err
}

// Send emits one explicit outbound message as a PRIVMSG.
func (c *RealClient) Send(msg protocol.OutboundMessage) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.writer == nil {
		return ErrDisconnected
	}
	body := msg.Body
	if msg.HumanInitiated {
		body = humanSayPrefix + body
	}
	for _, part := range splitBody(body) {
		line := "PRIVMSG " + msg.Target + " :" + part
		if _, err := c.writer.WriteString(line + "\r\n"); err != nil {
			return err
		}
	}
	return c.writer.Flush()
}

// Events delivers inbound messages until Disconnect.
func (c *RealClient) Events() <-chan protocol.InboundEvent { return c.events }

// PresenceEvent reports an IRC MONITOR online/offline notification.
type PresenceEvent struct {
	Nick   string
	Online bool
}

// Presence delivers MONITOR notifications until the client disconnects.
func (c *RealClient) Presence() <-chan PresenceEvent { return c.presence }

// Monitor subscribes to online/offline notifications for nick.
func (c *RealClient) Monitor(nick string) error {
	nick = strings.TrimSpace(nick)
	if nick == "" || strings.ContainsAny(nick, " ,\r\n") {
		return fmt.Errorf("invalid MONITOR nick %q", nick)
	}
	return c.write("MONITOR + " + nick)
}

func (c *RealClient) write(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writer == nil {
		return ErrDisconnected
	}
	if _, err := c.writer.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return c.writer.Flush()
}

// readLoop turns IRC lines into protocol events and answers keepalives.
func (c *RealClient) readLoop(conn net.Conn, rd *bufio.Reader) {
	defer func() {
		_ = c.Disconnect()
		close(c.events)
		close(c.presence)
	}()
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		msg := parseLine(line)

		switch msg.command {
		case "PING":
			payload := strings.Join(msg.params, " ")
			_ = c.write("PONG :" + payload)
		case "PRIVMSG":
			ev := c.eventFrom(msg)
			if ev == nil {
				continue
			}
			select {
			case c.events <- *ev:
			case <-c.done:
				return
			}
		case "730", "731":
			online := msg.command == "730"
			for _, nick := range monitorNicks(msg) {
				select {
				case c.presence <- PresenceEvent{Nick: nick, Online: online}:
				case <-c.done:
					return
				}
			}
		case "ERROR":
			return
		case "FAIL":
			c.reportServerError(msg)
		default:
			if len(msg.command) == 3 && (msg.command[0] == '4' || msg.command[0] == '5') {
				c.reportServerError(msg)
			}
		}
	}
}

func (c *RealClient) reportServerError(msg ircMessage) {
	if c.cfg.OnServerError == nil {
		return
	}
	detail := strings.TrimSpace(strings.Join(msg.params, " ") + " " + msg.trailing)
	c.cfg.OnServerError(msg.command, detail)
}

func monitorNicks(msg ircMessage) []string {
	value := msg.trailing
	if value == "" && len(msg.params) > 1 {
		value = msg.params[len(msg.params)-1]
	}
	var nicks []string
	for _, entry := range strings.Split(value, ",") {
		nick := strings.TrimSpace(entry)
		if nick == "" {
			continue
		}
		if bang := strings.IndexByte(nick, '!'); bang >= 0 {
			nick = nick[:bang]
		}
		if nick != "" {
			nicks = append(nicks, nick)
		}
	}
	return nicks
}

// eventFrom converts an inbound PRIVMSG into a protocol event. It returns nil
// for messages the endpoint should not process (self-sends, empty bodies).
func (c *RealClient) eventFrom(msg ircMessage) *protocol.InboundEvent {
	if len(msg.params) == 0 || msg.trailing == "" {
		return nil
	}
	target := msg.params[0]
	sender := identityFromPrefix(msg.prefix)
	if sender.Nick == c.cfg.Nick {
		return nil // never process our own output
	}

	body := msg.trailing
	humanInitiated := strings.HasPrefix(body, humanSayPrefix)
	if id, index, total, payload, ok := parseChunkWithID(body); ok {
		assembled, done := c.acceptChunk(sender.Nick, id, index, total, payload)
		if !done {
			return nil
		}
		body = assembled
	}
	if strings.HasPrefix(body, humanSayPrefix) {
		body = strings.TrimPrefix(body, humanSayPrefix)
		humanInitiated = true
	}

	ev := &protocol.InboundEvent{
		ID:             newEventID(),
		EndpointID:     protocol.EndpointID(c.cfg.Nick),
		Sender:         sender,
		Body:           body,
		SentAt:         nowUnix(),
		HumanInitiated: humanInitiated,
	}
	if strings.HasPrefix(target, "#") {
		ev.Kind = protocol.KindRoom
		ev.Target = target
		ev.Mentions = detectMentions(body, c.cfg.Nick)
	} else {
		ev.Kind = protocol.KindDM
		ev.Target = c.cfg.Nick
	}
	if err := ev.Validate(); err != nil {
		return nil
	}
	return ev
}

func (c *RealClient) acceptChunk(sender, id string, index, total int, payload string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for nick, pending := range c.chunks {
		if !pending.updatedAt.IsZero() && now.Sub(pending.updatedAt) > chunkTTL {
			delete(c.chunks, nick)
		}
	}
	if c.chunks == nil {
		c.chunks = make(map[string]*chunkAssembler)
	}
	a := c.chunks[sender]
	if a == nil || (a.total != 0 && a.total != total) {
		a = &chunkAssembler{}
		c.chunks[sender] = a
	}
	body, done := a.add(id, index, total, payload, now)
	if done {
		delete(c.chunks, sender)
	}
	return body, done
}

// detectMentions finds exact nick mentions in a message body. A mention is the
// nick delimited by non-word boundaries; "agent" never matches "agent-alpha".
func detectMentions(body, nick string) []string {
	if nick == "" {
		return nil
	}
	var found []string
	for _, word := range strings.FieldsFunc(body, func(r rune) bool {
		return !isWordRune(r)
	}) {
		if word == nick {
			return []string{nick}
		}
	}
	return found
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '-'
}

// identityFromPrefix extracts nick and account from an IRC prefix. The account
// is only trustworthy when the server included it (e.g. via extended-join or
// WHOIS); we never treat the nick alone as authenticated identity.
func identityFromPrefix(prefix string) protocol.Identity {
	var id protocol.Identity
	if prefix == "" {
		return id
	}
	if bang := strings.IndexByte(prefix, '!'); bang >= 0 {
		id.Nick = prefix[:bang]
	} else {
		id.Nick = prefix
	}
	return id
}
