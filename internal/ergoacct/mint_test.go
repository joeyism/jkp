package ergoacct

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestMintGuestNickFormat(t *testing.T) {
	input := append(bytes.Repeat([]byte{0x5a}, 32), []byte{0xab, 0xcd, 0xef, 0x01}...)
	m := Minter{Register: func(string, string) error { return nil }, Rand: bytes.NewReader(input)}
	nick, pass, err := m.Mint()
	if err != nil {
		t.Fatal(err)
	}
	if len(nick) != 9 || !strings.HasPrefix(nick, "g") || nick != "gabcdef01" {
		t.Fatalf("nick = %q, want g + 8 lowercase hex", nick)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(pass)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("password = %q, decoded length=%d err=%v; want 32 random bytes", pass, len(decoded), err)
	}
}

func TestMintGuestRegistersThenReturns(t *testing.T) {
	input := append(bytes.Repeat([]byte{0x31}, 32), []byte{1, 2, 3, 4}...)
	var registeredNick, registeredPass string
	m := Minter{Rand: bytes.NewReader(input), Register: func(nick, pass string) error {
		registeredNick, registeredPass = nick, pass
		return nil
	}}
	nick, pass, err := m.Mint()
	if err != nil || nick != registeredNick || pass != registeredPass {
		t.Fatalf("Mint=(%q,%q,%v), registered=(%q,%q)", nick, pass, err, registeredNick, registeredPass)
	}
}

func TestMintGuestRetriesOnExisting(t *testing.T) {
	input := append(bytes.Repeat([]byte{0x72}, 32), []byte{0x11, 0x11, 0x11, 0x11, 0x22, 0x22, 0x22, 0x22}...)
	var names []string
	m := Minter{Rand: bytes.NewReader(input), Register: func(nick, _ string) error {
		names = append(names, nick)
		if len(names) == 1 {
			return errAccountExists
		}
		return nil
	}}
	nick, _, err := m.Mint()
	if err != nil || nick != "g22222222" || strings.Join(names, ",") != "g11111111,g22222222" {
		t.Fatalf("Mint nick=%q err=%v register calls=%v", nick, err, names)
	}
}

func TestMintGuestRetriesOnReservedName(t *testing.T) {
	input := append(bytes.Repeat([]byte{0x42}, 32), []byte{0x33, 0x33, 0x33, 0x33, 0x44, 0x44, 0x44, 0x44}...)
	var calls int
	m := Minter{Rand: bytes.NewReader(input), Register: func(string, string) error {
		calls++
		if calls == 1 {
			return errAccountReserved
		}
		return nil
	}}
	nick, _, err := m.Mint()
	if err != nil || nick != "g44444444" || calls != 2 {
		t.Fatalf("Mint nick=%q err=%v calls=%d", nick, err, calls)
	}
}

func TestMintGuestPropagatesRegisterError(t *testing.T) {
	want := errors.New("oper unavailable")
	input := append(bytes.Repeat([]byte{0x51}, 32), []byte{1, 2, 3, 4}...)
	m := Minter{Rand: bytes.NewReader(input), Register: func(string, string) error { return want }}
	if _, _, err := m.Mint(); !errors.Is(err, want) {
		t.Fatalf("Mint error = %v, want %v", err, want)
	}
}

func TestMintGuestDoesNotRetryForever(t *testing.T) {
	input := append(bytes.Repeat([]byte{0x61}, 32), bytes.Repeat([]byte{0x77}, 4*3)...)
	calls := 0
	m := Minter{Rand: bytes.NewReader(input), MaxAttempts: 3, Register: func(string, string) error {
		calls++
		return errAccountExists
	}}
	_, _, err := m.Mint()
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || calls != 3 {
		t.Fatalf("Mint err=%v calls=%d", err, calls)
	}
}
