//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedOpenCode struct {
	server    *httptest.Server
	sessionID string
	password  string
	respond   func(turn int, input string) (string, bool)

	mu          sync.Mutex
	turns       int
	userCount   int
	users       []string
	userHistory []string
	message     []map[string]any
	nextID      int
}

func newScriptedOpenCode(t *testing.T, sessionID, password string, respond func(int, string) (string, bool)) *scriptedOpenCode {
	t.Helper()
	f := &scriptedOpenCode{
		sessionID: sessionID,
		password:  password,
		respond:   respond,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *scriptedOpenCode) URL() string { return f.server.URL }

func (f *scriptedOpenCode) serveHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || user != "opencode" || pass != f.password {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt_async"):
		var req struct {
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Parts) == 0 {
			http.Error(w, "bad prompt", http.StatusBadRequest)
			return
		}
		text := req.Parts[0].Text
		f.mu.Lock()
		f.turns++
		turn := f.turns
		f.userCount++
		f.nextID++
		userID := fmt.Sprintf("user-%d", f.nextID)
		f.users = append(f.users, text)
		f.userHistory = append(f.userHistory, text)
		f.message = append(f.message, map[string]any{
			"info":  map[string]any{"id": userID, "role": "user", "parentID": ""},
			"parts": []map[string]any{{"type": "text", "text": text}},
		})
		f.mu.Unlock()

		answer, send := f.respond(turn, text)
		if send {
			f.mu.Lock()
			f.nextID++
			assistantID := fmt.Sprintf("assistant-%d", f.nextID)
			f.message = append(f.message, map[string]any{
				"info":  map[string]any{"id": assistantID, "role": "assistant", "parentID": userID, "finish": "stop"},
				"parts": []map[string]any{{"type": "text", "text": answer}},
			})
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
		f.mu.Lock()
		messages := append([]map[string]any(nil), f.message...)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(messages)
	default:
		http.NotFound(w, r)
	}
}

func (f *scriptedOpenCode) WaitUser(ctx context.Context) string {
	ticker := make(chan struct{})
	go func() {
		for {
			f.mu.Lock()
			if len(f.users) > 0 {
				f.mu.Unlock()
				close(ticker)
				return
			}
			f.mu.Unlock()
			select {
			case <-ctx.Done():
				close(ticker)
				return
			default:
			}
			select {
			case <-ctx.Done():
				close(ticker)
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	select {
	case <-ticker:
	case <-ctx.Done():
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.users) == 0 {
		return ""
	}
	text := f.users[0]
	f.users = f.users[1:]
	return text
}

func (f *scriptedOpenCode) UserCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.userCount
}

func (f *scriptedOpenCode) AllUsers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.userHistory...)
}
