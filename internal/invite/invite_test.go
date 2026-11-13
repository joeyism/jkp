package invite_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/joey/jkp/internal/invite"
)

func TestInviteV2RoundTrip(t *testing.T) {
	t.Parallel()
	in := invite.Invite{
		Host: "192.168.0.62", Port: 6697, JoinNick: "join", JoinPassword: "s3cret/+=",
		Peer: "host", FP: strings.Repeat("ab", 32),
	}
	tok, err := invite.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 5 || tok[:5] != "jkp1." {
		t.Fatalf("token prefix: %q", tok)
	}
	out, err := invite.Decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	if in.V = 2; out != in {
		t.Fatalf("round trip: got %#v want %#v", out, in)
	}
}

func TestInviteV2RequiresJoinCredentials(t *testing.T) {
	t.Parallel()
	base := invite.Invite{Host: "host", Port: 6697, Peer: "host", FP: strings.Repeat("ab", 32)}
	for _, in := range []invite.Invite{base, {Host: base.Host, Port: base.Port, JoinNick: "join", Peer: base.Peer, FP: base.FP}} {
		if _, err := invite.Encode(in); err == nil || !strings.Contains(err.Error(), "incomplete invite") {
			t.Fatalf("Encode(%#v) error = %v, want incomplete invite", in, err)
		}
	}
}

func TestInviteV1IsRejectedWithResetHint(t *testing.T) {
	t.Parallel()
	old := `{"v":1,"host":"192.168.0.62","port":6697,"nick":"guest","password":"pw","peer":"host","fp":"` + strings.Repeat("ab", 32) + `"}`
	tok := "jkp1." + base64.RawURLEncoding.EncodeToString([]byte(old))
	if _, err := invite.Decode(tok); err == nil || !strings.Contains(err.Error(), "jkp reset") || !strings.Contains(err.Error(), "jkp host") {
		t.Fatalf("Decode(v1) error = %v, want reset and host instructions", err)
	}
}

func TestRejectsGarbage(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "jkp1.", "jkp1.@@@", "http://x", "jkp2.abc", "jkp1.e30"} {
		if _, err := invite.Decode(s); err == nil {
			t.Fatalf("Decode(%q) accepted garbage", s)
		}
	}
}

func TestEncodeRejectsIncomplete(t *testing.T) {
	t.Parallel()
	if _, err := invite.Encode(invite.Invite{Host: "h", Port: 1}); err == nil {
		t.Fatal("Encode accepted incomplete invite")
	}
}

func TestDecodeTrimSpace(t *testing.T) {
	t.Parallel()
	in := invite.Invite{
		Host: "10.0.0.2", Port: 6697,
		JoinNick: "join", JoinPassword: "p",
		Peer: "host", FP: strings.Repeat("cd", 32),
	}
	tok, err := invite.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := invite.Decode("  " + tok + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if in.V = 2; out != in {
		t.Fatalf("trimmed decode: %#v", out)
	}
}
