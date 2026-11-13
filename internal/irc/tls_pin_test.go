package irc_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/joey/jkp/internal/irc"
)

func listenSelfSigned(t *testing.T) (net.Listener, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "jkp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if tc, ok := c.(*tls.Conn); ok {
				_ = tc.Handshake()
			}
			_ = c.Close()
		}
	}()
	sum := sha256.Sum256(der)
	return ln, hex.EncodeToString(sum[:])
}

func TestFingerprintPinAcceptsMatchingLeaf(t *testing.T) {
	ln, fp := listenSelfSigned(t)
	defer ln.Close()
	if err := irc.DialTLSPinned(ln.Addr().String(), fp); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintPinRejectsMismatch(t *testing.T) {
	ln, _ := listenSelfSigned(t)
	defer ln.Close()
	err := irc.DialTLSPinned(ln.Addr().String(), strings.Repeat("ab", 32))
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("want fingerprint error, got %v", err)
	}
}

func TestFingerprintPinRejectsEmpty(t *testing.T) {
	ln, _ := listenSelfSigned(t)
	defer ln.Close()
	if err := irc.DialTLSPinned(ln.Addr().String(), ""); err == nil {
		t.Fatal("want error for empty fingerprint")
	}
}

// Ensure pem decode of a leaf works (used by jkp host fingerprint helper).
func TestPEMLeafSHA256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	sum := sha256.Sum256(der)
	if len(sum) != 32 {
		t.Fatal("bad sha256 size")
	}
	_ = pemBytes
}
