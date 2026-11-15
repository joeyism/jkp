package lan_test

import (
	"testing"

	"github.com/joey/jkp/internal/lan"
)

func TestPrefersDefaultRouteInterface(t *testing.T) {
	t.Parallel()
	got, err := lan.Choose(lan.Input{
		DefaultIface: "en0",
		Addrs: map[string][]string{
			"en0":     {"192.168.0.62/24"},
			"bridge0": {"172.20.0.1/16"},
			"lo0":     {"127.0.0.1/8"},
		},
	})
	if err != nil || got != "192.168.0.62" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestRejectsOnlyDockerAndLoopback(t *testing.T) {
	t.Parallel()
	_, err := lan.Choose(lan.Input{
		DefaultIface: "bridge0",
		Addrs: map[string][]string{
			"bridge0": {"172.20.0.1/16"},
			"lo0":     {"127.0.0.1/8"},
		},
	})
	if err == nil {
		t.Fatal("expected error when only docker and loopback exist")
	}
}

func TestAllowsPrivateOnDefaultInterface(t *testing.T) {
	t.Parallel()
	got, err := lan.Choose(lan.Input{
		DefaultIface: "en0",
		Addrs: map[string][]string{
			"en0": {"10.0.0.5/24"},
		},
	})
	if err != nil || got != "10.0.0.5" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestSkipsLinkLocal(t *testing.T) {
	t.Parallel()
	got, err := lan.Choose(lan.Input{
		DefaultIface: "en0",
		Addrs: map[string][]string{
			"en0": {"fe80::1/64", "192.168.1.10/24"},
		},
	})
	if err != nil || got != "192.168.1.10" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestFallsBackToOtherInterfaces(t *testing.T) {
	t.Parallel()
	got, err := lan.Choose(lan.Input{
		DefaultIface: "en0",
		Addrs: map[string][]string{
			"en0": {"172.20.0.1/16"},
			"en1": {"192.168.0.100/24"},
		},
	})
	if err != nil || got != "192.168.0.100" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestFallbackPrefersPhysicalInterfaceOverNamedBridges(t *testing.T) {
	in := lan.Input{
		Addrs: map[string][]string{
			"docker0": {"172.17.0.1/16"},
			"br-abc":  {"192.168.64.1/20"},
			"wlp1s0":  {"192.168.1.153/24"},
			"en0":     {"10.0.0.5/24"},
		},
	}
	for i := 0; i < 50; i++ {
		got, err := lan.Choose(in)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if got == "192.168.64.1" || got == "172.17.0.1" {
			t.Fatalf("iteration %d: picked bridge %q", i, got)
		}
	}
}
