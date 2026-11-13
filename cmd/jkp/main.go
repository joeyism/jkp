// Command jkp is the collaboration core: it binds harness adapters to IRC
// participants. See docs/architecture/ for the contract and threat model.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joey/jkp/internal/cli"
	"github.com/joey/jkp/internal/core"
	"github.com/joey/jkp/internal/protocol"
	"github.com/joey/jkp/internal/store"
)

// nopSender is the transport until an IRC client is attached by serve. It
// refuses sends rather than silently dropping them.
type nopSender struct{}

func (nopSender) Send(protocol.OutboundMessage) error {
	return fmt.Errorf("no transport attached")
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// statePath is where endpoint registration and replay cursors survive
// restarts. It is created with restrictive permissions by the store.
func statePath() string {
	if p := os.Getenv("JKP_STATE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "jkp.db"
	}
	return filepath.Join(home, ".jkp", "state.db")
}

func main() {
	svc := core.NewService(nopSender{}, wallClock{})

	// Restore previously registered endpoints so a restart keeps addressing.
	if st, err := store.Open(statePath()); err == nil {
		defer func() { _ = st.Close() }()
		if eps, err := st.LoadEndpoints(); err == nil {
			for _, ep := range eps {
				_ = svc.Register(ep)
			}
		}
	}

	c := cli.New(svc, os.Stdout, os.Stderr)
	if err := c.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jkp:", err)
		os.Exit(1)
	}

	// Persist any registration made this invocation.
	if st, err := store.Open(statePath()); err == nil {
		defer func() { _ = st.Close() }()
		for _, ep := range svc.List() {
			_ = st.SaveEndpoint(ep)
		}
	}
}
