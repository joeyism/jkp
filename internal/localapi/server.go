// Package localapi is the authenticated loopback surface harness adapters use
// to register an endpoint, receive inbound events, and send explicit replies.
// It deliberately exposes no raw harness control: adapters get conversation
// traffic only, never shell, edit, or provider access.
package localapi

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
)

// DefaultListenAddr is the only address the local API binds by default. It is
// loopback-only; exposing it elsewhere is a deliberate, documented change.
const DefaultListenAddr = "127.0.0.1:8443"

// DefaultLongPollTimeout bounds how long /v1/events waits for work.
const DefaultLongPollTimeout = 15 * time.Second

// DefaultListenAddrIsLoopback reports whether the default bind address is
// loopback-only. This is asserted in tests so a regression is loud.
func DefaultListenAddrIsLoopback() bool {
	host, _, err := net.SplitHostPort(DefaultListenAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Server exposes the adapter API over HTTP. It authenticates with one local
// bearer token and binds to loopback.
type Server struct {
	svc *core.Service

	token            string
	LongPollTimeout  time.Duration
	pollInterval     time.Duration
	outboundSequence atomic.Uint64
}

// NewServer returns a server delegating to svc and requiring token.
func NewServer(svc *core.Service, token string) *Server {
	return &Server{
		svc:             svc,
		token:           token,
		LongPollTimeout: DefaultLongPollTimeout,
		pollInterval:    2 * time.Millisecond,
	}
}

// Handler returns the HTTP handler for this server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/register", s.auth(s.handleRegister))
	mux.HandleFunc("POST /v1/enable", s.auth(s.handleEnable))
	mux.HandleFunc("POST /v1/disable", s.auth(s.handleDisable))
	mux.HandleFunc("GET /v1/events", s.auth(s.handleEvents))
	mux.HandleFunc("POST /v1/reply", s.auth(s.handleReply))
	mux.HandleFunc("POST /v1/finish", s.auth(s.handleFinish))
	mux.HandleFunc("POST /v1/send", s.auth(s.handleSend))
	// Health is authenticated too: this API fails closed everywhere rather
	// than leaving one unauthenticated edge to reason about.
	mux.HandleFunc("GET /v1/health", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || got != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

type registerRequest struct {
	ID        string `json:"id"`
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
	Nick      string `json:"nick"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.SessionID == "" || req.Nick == "" {
		http.Error(w, "id, session_id and nick are required", http.StatusBadRequest)
		return
	}
	err := s.svc.Register(core.Endpoint{
		ID:        protocol.EndpointID(req.ID),
		Harness:   req.Harness,
		SessionID: req.SessionID,
		Nick:      req.Nick,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type idRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleEnable(w http.ResponseWriter, r *http.Request) {
	s.toggle(w, r, (*core.Service).Enable)
}

func (s *Server) handleDisable(w http.ResponseWriter, r *http.Request) {
	s.toggle(w, r, (*core.Service).Disable)
}

func (s *Server) toggle(w http.ResponseWriter, r *http.Request, fn func(*core.Service, protocol.EndpointID) error) {
	var req idRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if err := fn(s.svc, protocol.EndpointID(req.ID)); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents long-polls for the next inbound event. It returns 200 with the
// event, 204 when nothing arrives before the timeout, and 404 for an unknown
// endpoint.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := protocol.EndpointID(r.URL.Query().Get("endpoint_id"))
	if id == "" {
		http.Error(w, "endpoint_id is required", http.StatusBadRequest)
		return
	}
	if _, ok := s.svc.Get(id); !ok {
		http.Error(w, "unknown endpoint", http.StatusNotFound)
		return
	}

	deadline := time.Now().Add(s.LongPollTimeout)
	for {
		if ev, ok := s.svc.Next(id); ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ev)
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusNoContent)
			return
		case <-time.After(s.pollInterval):
		}
	}
}

type replyRequest struct {
	EndpointID string `json:"endpoint_id"`
	InReplyTo  string `json:"in_reply_to"`
	Body       string `json:"body"`
}

// handleReply sends one explicit reply. The conversation coordinate is derived
// from the inbound event being answered, so an adapter cannot redirect a reply
// into another conversation.
func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	var req replyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.InReplyTo == "" {
		http.Error(w, "in_reply_to is required", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		http.Error(w, "body is required", http.StatusBadRequest)
		return
	}
	id := protocol.EndpointID(req.EndpointID)
	orig, ok := s.svc.Event(id, req.InReplyTo)
	if !ok {
		http.Error(w, "unknown inbound event", http.StatusNotFound)
		return
	}

	msg := protocol.OutboundMessage{
		ID:         s.nextOutboundID(),
		EndpointID: id,
		Kind:       orig.Kind,
		Target:     orig.ReplyTarget(),
		Body:       req.Body,
		InReplyTo:  req.InReplyTo,
	}
	if err := s.svc.Reply(msg); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type finishRequest struct {
	EndpointID string `json:"endpoint_id"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
}

type sendRequest struct {
	FromEndpointID string `json:"from_endpoint_id"`
	ToEndpointID   string `json:"to_endpoint_id"`
	Body           string `json:"body"`
}

// handleSend lets one endpoint initiate a message to another. This is the
// agent-to-agent outbound path: an adapter translates its agent's intent to
// reach a peer into a new inbound event for that peer, attributed to the
// initiating endpoint so replies route back to it.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Body == "" {
		http.Error(w, "body is required", http.StatusBadRequest)
		return
	}
	from := protocol.EndpointID(req.FromEndpointID)
	to := protocol.EndpointID(req.ToEndpointID)
	if from == "" || to == "" {
		http.Error(w, "from_endpoint_id and to_endpoint_id are required", http.StatusBadRequest)
		return
	}
	// Self-addressing would create a self-triggering loop.
	if from == to {
		http.Error(w, "an endpoint cannot send to itself", http.StatusBadRequest)
		return
	}
	fromEp, ok := s.svc.Get(from)
	if !ok {
		http.Error(w, "unknown sending endpoint", http.StatusNotFound)
		return
	}
	toEp, ok := s.svc.Get(to)
	if !ok {
		http.Error(w, "unknown target endpoint", http.StatusNotFound)
		return
	}

	ev := protocol.InboundEvent{
		ID:         s.nextOutboundID(),
		EndpointID: to,
		Kind:       protocol.KindDM,
		Target:     toEp.Nick,
		// The sender is the initiating endpoint, so a reply routes back to it.
		Sender: protocol.Identity{Nick: fromEp.Nick, Account: string(from)},
		Body:   req.Body,
	}
	if err := s.svc.Admit(ev); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.EndpointID == "" || req.Kind == "" || req.Target == "" {
		http.Error(w, "endpoint_id, kind and target are required", http.StatusBadRequest)
		return
	}
	err := s.svc.Finish(protocol.EndpointID(req.EndpointID), protocol.Conversation{
		Kind: protocol.Kind(req.Kind), Target: req.Target,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) nextOutboundID() string {
	n := s.outboundSequence.Add(1)
	return "out-" + time.Now().UTC().Format("20060102150405") + "-" + itoa(n)
}

func itoa(n uint64) string {
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
