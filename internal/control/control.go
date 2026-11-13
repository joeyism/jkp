// Package control manages the authenticated loopback channel used by jkp say.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joey/jkp/internal/protocol"
	"github.com/joey/jkp/internal/roster"
)

const (
	HostFileName    = "agent-host.json"
	GuestFileName   = "agent-guest.json"
	GuestFilePrefix = "agent-guest-"
	LegacyFileName  = "agent.json"
)

// Info describes the local agent's control endpoint and room role.
type Info struct {
	Addr        string `json:"addr"`
	Token       string `json:"token"`
	Role        string `json:"role"`
	Nick        string `json:"nick"`
	Peer        string `json:"peer"`
	PID         int    `json:"pid"`
	SessionID   string `json:"session_id"`
	OpenCodeURL string `json:"opencode_url,omitempty"`
	LogFile     string `json:"log_file,omitempty"`
}

// AgentPaths returns role-specific files in host-then-guest order.
func AgentPaths(dir string) []string {
	paths := []string{filepath.Join(dir, HostFileName)}
	guests, _ := filepath.Glob(filepath.Join(dir, GuestFilePrefix+"*.json"))
	paths = append(paths, guests...)
	paths = append(paths, filepath.Join(dir, GuestFileName))
	return paths
}

// LegacyPath returns the pre-role-split shared control file path.
func LegacyPath(dir string) string { return filepath.Join(dir, LegacyFileName) }

// Path returns the role-specific control file for a host or guest.
func Path(dir, role string, nick ...string) (string, error) {
	switch role {
	case "host":
		return filepath.Join(dir, HostFileName), nil
	case "guest":
		if len(nick) > 0 && nick[0] != "" {
			if strings.ContainsAny(nick[0], "/\\\r\n") || nick[0] == "." || nick[0] == ".." {
				return "", fmt.Errorf("invalid guest nick %q", nick[0])
			}
			return filepath.Join(dir, GuestFilePrefix+nick[0]+".json"), nil
		}
		return filepath.Join(dir, GuestFileName), nil
	default:
		return "", fmt.Errorf("unknown jkp role %q", role)
	}
}

// IsProcessAlive reports whether pid currently identifies a running process.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Save stores private control metadata at path.
func Save(path string, info Info) error {
	if err := validateInfo(info); err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// Load reads local control metadata and provides actionable missing-state help.
func Load(path string) (Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Info{}, fmt.Errorf("not in a room; run jkp host or jkp join first (jkp help)")
		}
		return Info{}, fmt.Errorf("read agent control file: %w", err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("parse agent control file: %w", err)
	}
	if err := validateInfo(info); err != nil {
		return Info{}, err
	}
	return info, nil
}

func validateInfo(info Info) error {
	if info.Addr == "" || info.Token == "" || info.Role == "" || info.Nick == "" || info.PID <= 0 || info.SessionID == "" {
		return fmt.Errorf("agent control file is incomplete; run jkp reset, then join again")
	}
	if info.Role == "guest" && info.Peer == "" {
		return fmt.Errorf("guest control file is missing peer")
	}
	if info.Role != "host" && info.Role != "guest" {
		return fmt.Errorf("unknown agent control role %q", info.Role)
	}
	return nil
}

// LoadAgents reads all role-specific control files that currently exist.
func LoadAgents(dir string) ([]Info, error) {
	var infos []Info
	paths := append(AgentPaths(dir), LegacyPath(dir))
	for _, path := range paths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var info Info
		if err := json.Unmarshal(data, &info); err != nil {
			return nil, fmt.Errorf("parse agent control file: %w", err)
		}
		if !IsProcessAlive(info.PID) {
			continue
		}
		if path == LegacyPath(dir) {
			return nil, fmt.Errorf("live jkp agent uses an older shared control file; run jkp reset")
		}
		base := filepath.Base(path)
		if base == GuestFileName {
			return nil, fmt.Errorf("live jkp guest uses an older control file; run jkp reset")
		}
		if base == HostFileName && info.Role != "host" || strings.HasPrefix(base, GuestFilePrefix) && info.Role != "guest" {
			return nil, fmt.Errorf("agent control role does not match its filename; run jkp reset")
		}
		if strings.HasPrefix(base, GuestFilePrefix) && base != GuestFilePrefix+info.Nick+".json" {
			return nil, fmt.Errorf("guest control nick does not match its filename; run jkp reset")
		}
		if err := validateInfo(info); err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Client sends a message through the already-connected local agent.
type Client struct {
	InfoPath string
	HTTP     *http.Client
}

// NewClient returns a client reading agent metadata from infoPath.
func NewClient(infoPath string) *Client {
	return &Client{InfoPath: infoPath, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

type message struct {
	Body string `json:"body"`
	To   string `json:"to,omitempty"`
}

type sendResult struct {
	Target string `json:"target"`
}

type routeErrorResponse struct {
	Error  string   `json:"error"`
	Guests []string `json:"guests,omitempty"`
	Last   string   `json:"last,omitempty"`
}

// RouteError is a safe-to-display error returned when a message cannot be
// routed to a connected guest.
type RouteError struct {
	Message string
	Guests  []string
	Last    string
}

func (e *RouteError) Error() string { return e.Message }

type rosterResponse struct {
	Guests []string `json:"guests"`
	Last   string   `json:"last"`
}

// Send sends body to the peer through the authenticated loopback agent.
func (c *Client) Send(ctx context.Context, to, body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("message is empty; usage: jkp say -- <text> (jkp help)")
	}
	info, err := Load(c.InfoPath)
	if err != nil {
		return "", err
	}
	resp, err := c.request(ctx, info, http.MethodPost, "/v1/send", message{Body: body, To: to})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		var route routeErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&route); err != nil {
			return "", fmt.Errorf("local jkp agent: %s", resp.Status)
		}
		return "", &RouteError{Message: route.Error, Guests: route.Guests, Last: route.Last}
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("local jkp agent: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var result sendResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode local jkp send response: %w", err)
	}
	if result.Target == "" {
		return "", fmt.Errorf("local jkp agent returned an empty target")
	}
	return result.Target, nil
}

// Roster returns the live agent's guest list and last speaker.
func (c *Client) Roster(ctx context.Context) (roster.Snapshot, error) {
	info, err := Load(c.InfoPath)
	if err != nil {
		return roster.Snapshot{}, err
	}
	resp, err := c.request(ctx, info, http.MethodGet, "/v1/roster", nil)
	if err != nil {
		return roster.Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return roster.Snapshot{}, fmt.Errorf("local jkp agent: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var result rosterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return roster.Snapshot{}, err
	}
	return roster.Snapshot{Guests: result.Guests, Last: result.Last}, nil
}

func (c *Client) request(ctx context.Context, info Info, method, path string, body any) (*http.Response, error) {
	u, err := url.Parse(info.Addr)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid local agent address")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("refusing non-loopback agent address")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(info.Addr, "/")+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.Token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send through local jkp agent: %w; run jkp reset and rejoin if the agent stopped", err)
	}
	return resp, nil
}

type Handlers struct {
	Role   string
	Peer   string
	Send   func(to, body string) (string, error)
	Roster func() roster.Snapshot
}

// NewHandler accepts authenticated send and roster requests and delegates
// sends to the live agent's established IRC connection.
func NewHandler(token string, handlers Handlers) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req message
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.Body = strings.TrimSpace(req.Body)
		if req.Body == "" {
			http.Error(w, "body is required", http.StatusBadRequest)
			return
		}
		if len(req.Body) > protocol.MaxBodyBytes {
			writeRouteError(w, &RouteError{Message: fmt.Sprintf(
				"not sent: message is %d bytes (max %d); split it into multiple 'jkp say' messages of <= %d bytes each",
				len(req.Body), protocol.MaxBodyBytes, protocol.SafeSplitBytes,
			)})
			return
		}
		if handlers.Role == "guest" {
			if req.To != "" && req.To != handlers.Peer {
				writeRouteError(w, &RouteError{Message: "guests can only message the host"})
				return
			}
			req.To = handlers.Peer
		}
		if handlers.Send == nil {
			http.Error(w, "send unavailable", http.StatusServiceUnavailable)
			return
		}
		target, err := handlers.Send(req.To, req.Body)
		if err != nil {
			var routeErr *RouteError
			if errors.As(err, &routeErr) {
				writeRouteError(w, routeErr)
				return
			}
			var rosterErr *roster.RouteError
			if errors.As(err, &rosterErr) {
				writeRouteError(w, &RouteError{Message: rosterErr.Error(), Guests: rosterErr.Guests, Last: rosterErr.Last})
				return
			}
			http.Error(w, "not sent: IRC transport failed: "+err.Error()+"; check 'jkp log' and rejoin if disconnected", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(sendResult{Target: target})
	})
	mux.HandleFunc("GET /v1/roster", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var snap roster.Snapshot
		if handlers.Roster != nil {
			snap = handlers.Roster()
		}
		_ = json.NewEncoder(w).Encode(rosterResponse{Guests: snap.Guests, Last: snap.Last})
	})
	return mux
}

func writeRouteError(w http.ResponseWriter, err *RouteError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(routeErrorResponse{Error: err.Error(), Guests: err.Guests, Last: err.Last})
}
