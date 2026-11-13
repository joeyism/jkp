package ergoacct

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Minter registers short-lived, uniquely named guest accounts for a shared
// room invite. Register must be strict: it must never change an existing
// account's password.
type Minter struct {
	Register    func(nick, password string) error
	Rand        io.Reader
	MaxAttempts int
}

func (m Minter) Mint() (nick, password string, err error) {
	if m.Register == nil {
		return "", "", fmt.Errorf("guest account registrar is required")
	}
	rng := m.Rand
	if rng == nil {
		rng = rand.Reader
	}
	var secret [32]byte
	if _, err := io.ReadFull(rng, secret[:]); err != nil {
		return "", "", fmt.Errorf("generate guest password: %w", err)
	}
	password = base64.RawURLEncoding.EncodeToString(secret[:])
	attempts := m.MaxAttempts
	if attempts <= 0 {
		attempts = 8
	}
	for i := 0; i < attempts; i++ {
		var suffix [4]byte
		if _, err := io.ReadFull(rng, suffix[:]); err != nil {
			return "", "", fmt.Errorf("generate guest nick: %w", err)
		}
		nick = "g" + hex.EncodeToString(suffix[:])
		if err := m.Register(nick, password); err == nil {
			return nick, password, nil
		} else if !errors.Is(err, errAccountExists) && !errors.Is(err, errAccountReserved) {
			return "", "", err
		} else if i == attempts-1 {
			return "", "", fmt.Errorf("unable to mint guest account after %d attempts: %w", attempts, err)
		}
	}
	return "", "", fmt.Errorf("unable to mint guest account")
}
