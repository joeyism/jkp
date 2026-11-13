// Package invite encodes and decodes a single pasteable token that carries
// everything a guest needs to request a minted LAN room identity: host, TLS
// port, shared join nick and password, peer nick, and certificate fingerprint.
package invite

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Invite is the decoded payload of a jkp1. token.
type Invite struct {
	V            int    `json:"v"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	JoinNick     string `json:"join_nick"`
	JoinPassword string `json:"join_password"`
	Peer         string `json:"peer"`
	FP           string `json:"fp"`
}

// Encode serialises in as a jkp1.<base64url-json> token.
func Encode(in Invite) (string, error) {
	in.V = 2
	if err := in.validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return "jkp1." + base64.RawURLEncoding.EncodeToString(b), nil
}

// Decode parses a jkp1. token back into an Invite. Surrounding whitespace is
// ignored so a paste from a chat window works.
func Decode(tok string) (Invite, error) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(tok), "jkp1.")
	if !ok || raw == "" {
		return Invite{}, fmt.Errorf("not a jkp invite")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Invite{}, fmt.Errorf("invite decode: %w", err)
	}
	var in Invite
	if err := json.Unmarshal(b, &in); err != nil {
		return Invite{}, fmt.Errorf("invite payload: %w", err)
	}
	if in.V != 2 {
		if in.V == 1 {
			return Invite{}, fmt.Errorf("older jkp invite; run jkp reset on the host, then jkp host to print a new invite")
		}
		return Invite{}, fmt.Errorf("invite version %d", in.V)
	}
	if err := in.validate(); err != nil {
		return Invite{}, err
	}
	return in, nil
}

func (in Invite) validate() error {
	if in.Host == "" || in.Port == 0 || in.JoinNick == "" || in.JoinPassword == "" || in.Peer == "" || len(in.FP) != 64 {
		return fmt.Errorf("incomplete invite")
	}
	return nil
}
