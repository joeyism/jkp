//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"os"
	"testing"

	"github.com/joey/jkp/internal/invite"
	"github.com/joey/jkp/internal/irc"
)

// TestLanInvitePin verifies the TLS fingerprint pin that jkp join uses.
// Set JKP_ERGO_TLS_ADDR to the server's routable address (host LAN IP:6697)
// and JKP_ERGO_CERT to the fullchain.pem path.
func TestLanInvitePin(t *testing.T) {
	addr := os.Getenv("JKP_ERGO_TLS_ADDR")
	if addr == "" {
		t.Skip("set JKP_ERGO_TLS_ADDR (host-lan-ip:6697) to run the LAN invite pin test")
	}
	certPath := os.Getenv("JKP_ERGO_CERT")
	if certPath == "" {
		t.Skip("set JKP_ERGO_CERT to the fullchain.pem path")
	}

	// Compute fingerprint from the PEM.
	pemData, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := irc.FingerprintPEM(pemData)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fingerprint: %s", fp)

	// Pin accept: matching fingerprint must succeed.
	if err := irc.DialTLSPinned(addr, fp); err != nil {
		t.Fatalf("pin accept: %v", err)
	}

	// Pin reject: flipped fingerprint must fail with "fingerprint".
	wrong := "ab" + fp[2:]
	if err := irc.DialTLSPinned(addr, wrong); err == nil {
		t.Fatal("pin reject: want error")
	}

	// Full invite round-trip.
	tok, err := invite.Encode(invite.Invite{
		Host:         "192.168.0.62",
		Port:         6697,
		JoinNick:     "join",
		JoinPassword: "join-pw",
		Peer:         "oc",
		FP:           fp,
	})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := invite.Decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	if inv.FP != fp {
		t.Fatalf("invite FP %q, want %q", inv.FP, fp)
	}

	// If JKP_PI_PASSWORD is set, do a real SASL connect over TLS.
	pw := os.Getenv("JKP_PI_PASSWORD")
	if pw == "" {
		t.Skip("set JKP_PI_PASSWORD to verify SASL over TLS")
	}
	_ = pem.Block{} // keep pem import
	sum := sha256.Sum256(nil)
	_ = hex.EncodeToString(sum[:])

	// Real TLS connect with pinned cert and SASL.
	cfg := irc.RealConfig{
		Host:     inv.Host,
		Port:     inv.Port,
		TLS:      true,
		CertFP:   inv.FP,
		Nick:     inv.JoinNick,
		Account:  inv.JoinNick,
		Password: pw,
	}
	client := irc.NewRealClient(cfg)
	if err := client.Connect(nil); err != nil {
		t.Fatalf("TLS SASL connect: %v", err)
	}
	_ = client.Disconnect()
}
