// Package joinproto encodes the small IRC protocol used to mint a guest
// identity from a shared room invite.
package joinproto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrNotMine = errors.New("grant is for another join request")

type Request struct {
	Nonce     string
	PublicKey string
}

type Grant struct {
	Nick     string `json:"nick"`
	Password string `json:"password"`
}

type SealedGrant struct {
	Nonce      string
	PublicKey  string
	GCMNonce   string
	Ciphertext string
}

type Deny struct {
	Nonce  string
	Reason string
}

func NewRequest() (Request, *ecdh.PrivateKey, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Request{}, nil, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Request{}, nil, err
	}
	return Request{
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce[:]),
		PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
	}, private, nil
}

func FormatJoin(req Request) string {
	return "JKP-JOIN " + req.Nonce + " " + req.PublicKey
}

func ParseJoin(line string) (Request, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 || fields[0] != "JKP-JOIN" || fields[1] == "" {
		return Request{}, fmt.Errorf("invalid JKP-JOIN")
	}
	pubBytes, err := base64.RawURLEncoding.DecodeString(fields[2])
	if err != nil || len(pubBytes) != 32 {
		return Request{}, fmt.Errorf("invalid JKP-JOIN public key")
	}
	if _, err := ecdh.X25519().NewPublicKey(pubBytes); err != nil {
		return Request{}, fmt.Errorf("invalid JKP-JOIN public key: %w", err)
	}
	return Request{Nonce: fields[1], PublicKey: fields[2]}, nil
}

func SealGrant(req Request, grant Grant) (string, error) {
	if req.Nonce == "" || grant.Nick == "" || grant.Password == "" {
		return "", fmt.Errorf("incomplete join grant")
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(req.PublicKey)
	if err != nil {
		return "", fmt.Errorf("decode join public key: %w", err)
	}
	public, err := ecdh.X25519().NewPublicKey(publicBytes)
	if err != nil {
		return "", fmt.Errorf("parse join public key: %w", err)
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := ephemeral.ECDH(public)
	if err != nil {
		return "", fmt.Errorf("derive grant secret: %w", err)
	}
	key, err := hkdf.Key(sha256.New, shared, []byte(req.Nonce), "jkp-grant-v1", 32)
	if err != nil {
		return "", fmt.Errorf("derive grant key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	gcmNonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(gcmNonce); err != nil {
		return "", err
	}
	plain, err := json.Marshal(grant)
	if err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, gcmNonce, plain, []byte(req.Nonce))
	return strings.Join([]string{
		"JKP-GRANT", req.Nonce,
		base64.RawURLEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(gcmNonce),
		base64.RawURLEncoding.EncodeToString(sealed),
	}, " "), nil
}

func ParseGrant(line string) (SealedGrant, error) {
	fields := strings.Fields(line)
	if len(fields) != 5 || fields[0] != "JKP-GRANT" {
		return SealedGrant{}, fmt.Errorf("invalid JKP-GRANT")
	}
	for i := 1; i < len(fields); i++ {
		if _, err := base64.RawURLEncoding.DecodeString(fields[i]); err != nil {
			return SealedGrant{}, fmt.Errorf("invalid JKP-GRANT field: %w", err)
		}
	}
	pub, _ := base64.RawURLEncoding.DecodeString(fields[2])
	if len(pub) != 32 {
		return SealedGrant{}, fmt.Errorf("invalid JKP-GRANT public key")
	}
	if _, err := ecdh.X25519().NewPublicKey(pub); err != nil {
		return SealedGrant{}, fmt.Errorf("invalid JKP-GRANT public key: %w", err)
	}
	return SealedGrant{Nonce: fields[1], PublicKey: fields[2], GCMNonce: fields[3], Ciphertext: fields[4]}, nil
}

func OpenGrant(line, requestNonce string, private *ecdh.PrivateKey) (Grant, error) {
	sealed, err := ParseGrant(line)
	if err != nil {
		return Grant{}, err
	}
	if sealed.Nonce != requestNonce {
		return Grant{}, ErrNotMine
	}
	if private == nil {
		return Grant{}, fmt.Errorf("missing join private key")
	}
	publicBytes, _ := base64.RawURLEncoding.DecodeString(sealed.PublicKey)
	public, err := ecdh.X25519().NewPublicKey(publicBytes)
	if err != nil {
		return Grant{}, err
	}
	shared, err := private.ECDH(public)
	if err != nil {
		return Grant{}, err
	}
	key, err := hkdf.Key(sha256.New, shared, []byte(requestNonce), "jkp-grant-v1", 32)
	if err != nil {
		return Grant{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Grant{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Grant{}, err
	}
	gcmNonce, _ := base64.RawURLEncoding.DecodeString(sealed.GCMNonce)
	if len(gcmNonce) != aead.NonceSize() {
		return Grant{}, fmt.Errorf("invalid JKP-GRANT nonce")
	}
	ciphertext, _ := base64.RawURLEncoding.DecodeString(sealed.Ciphertext)
	plain, err := aead.Open(nil, gcmNonce, ciphertext, []byte(requestNonce))
	if err != nil {
		return Grant{}, fmt.Errorf("open sealed guest grant: %w", err)
	}
	var grant Grant
	if err := json.Unmarshal(plain, &grant); err != nil {
		return Grant{}, fmt.Errorf("decode guest grant: %w", err)
	}
	if grant.Nick == "" || grant.Password == "" {
		return Grant{}, fmt.Errorf("incomplete guest grant")
	}
	return grant, nil
}

func FormatDeny(nonce, reason string) string {
	return "JKP-DENY " + nonce + " " + strings.Join(strings.Fields(reason), " ")
}

func ParseDeny(line string) (Deny, error) {
	fields := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(fields) != 3 || fields[0] != "JKP-DENY" || fields[1] == "" || strings.TrimSpace(fields[2]) == "" {
		return Deny{}, fmt.Errorf("invalid JKP-DENY")
	}
	return Deny{Nonce: fields[1], Reason: strings.TrimSpace(fields[2])}, nil
}

func IsProtocol(line string) bool {
	for _, command := range []string{"JKP-JOIN", "JKP-GRANT", "JKP-DENY"} {
		if line == command || strings.HasPrefix(line, command+" ") {
			return true
		}
	}
	return false
}
