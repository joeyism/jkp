package joinproto

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestJoinRequestRoundTrip(t *testing.T) {
	req, private, err := NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	line := FormatJoin(req)
	if !strings.HasPrefix(line, "JKP-JOIN ") {
		t.Fatalf("join line = %q", line)
	}
	got, err := ParseJoin(line)
	if err != nil || got != req {
		t.Fatalf("ParseJoin = %#v, %v; want %#v", got, err, req)
	}
	if len(private.Bytes()) != 32 {
		t.Fatalf("private key length = %d, want 32", len(private.Bytes()))
	}
}

func TestSealOpenGrant(t *testing.T) {
	req, private, err := NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	want := Grant{Nick: "g1a2b3c4", Password: "secret"}
	line, err := SealGrant(req, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenGrant(line, req.Nonce, private)
	if err != nil || got != want {
		t.Fatalf("OpenGrant = %#v, %v; want %#v", got, err, want)
	}
}

func TestOpenGrantWrongKeyFails(t *testing.T) {
	req, _, err := NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	line, err := SealGrant(req, Grant{Nick: "g1", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGrant(line, req.Nonce, wrong); err == nil {
		t.Fatal("OpenGrant succeeded with wrong private key")
	}
}

func TestOpenGrantTamperedFails(t *testing.T) {
	req, private, err := NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	line, err := SealGrant(req, Grant{Nick: "g1", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(line, " ")
	sealed, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	parts[4] = base64.RawURLEncoding.EncodeToString(sealed)
	if _, err := OpenGrant(strings.Join(parts, " "), req.Nonce, private); err == nil {
		t.Fatal("OpenGrant succeeded with tampered ciphertext")
	}
}

func TestOpenGrantOtherNonceIsNotMine(t *testing.T) {
	req, private, err := NewRequest()
	if err != nil {
		t.Fatal(err)
	}
	line, err := SealGrant(req, Grant{Nick: "g1", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGrant(line, "another-request", private); err != ErrNotMine {
		t.Fatalf("OpenGrant error = %v, want ErrNotMine", err)
	}
}

func TestDenyRoundTrip(t *testing.T) {
	line := FormatDeny("nonce", "account mint failed: account unavailable")
	got, err := ParseDeny(line)
	if err != nil || got != (Deny{Nonce: "nonce", Reason: "account mint failed: account unavailable"}) {
		t.Fatalf("ParseDeny = %#v, %v", got, err)
	}
}

func TestFormatDenyFlattensMultilineReason(t *testing.T) {
	got, err := ParseDeny(FormatDeny("nonce", "line one\r\nline two"))
	if err != nil || got.Reason != "line one line two" {
		t.Fatalf("ParseDeny = %#v, %v", got, err)
	}
}

func TestIsProtocolRecognizesOnlyLeadingProtocolCommands(t *testing.T) {
	for _, line := range []string{"JKP-JOIN n k", "JKP-GRANT n k c", "JKP-DENY n reason"} {
		if !IsProtocol(line) {
			t.Errorf("IsProtocol(%q) = false", line)
		}
	}
	for _, line := range []string{"hello", "please say JKP-JOIN in docs", "JKP-JOINED maybe"} {
		if IsProtocol(line) {
			t.Errorf("IsProtocol(%q) = true", line)
		}
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, line := range []string{"", "JKP-JOIN", "JKP-JOIN n", "JKP-JOIN n !!!", "JKP-GRANT n p", "JKP-DENY"} {
		if strings.HasPrefix(line, "JKP-JOIN") {
			if _, err := ParseJoin(line); err == nil {
				t.Errorf("ParseJoin(%q) accepted garbage", line)
			}
		} else if strings.HasPrefix(line, "JKP-GRANT") {
			if _, err := ParseGrant(line); err == nil {
				t.Errorf("ParseGrant(%q) accepted garbage", line)
			}
		} else if strings.HasPrefix(line, "JKP-DENY") {
			if _, err := ParseDeny(line); err == nil {
				t.Errorf("ParseDeny(%q) accepted garbage", line)
			}
		}
	}
}
