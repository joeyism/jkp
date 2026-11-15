package lan

import (
	"net"
	"testing"
)

func TestDetectWorksWithoutExternalCommands(t *testing.T) {
	t.Setenv("PATH", "")
	ip, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		t.Fatalf("want IPv4, got %q", ip)
	}
	if parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
		t.Fatalf("want routable IPv4, got %q", ip)
	}
	v4 := parsed.To4()
	if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
		t.Fatalf("want non-docker IPv4, got %q", ip)
	}
}
