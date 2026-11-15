// Package lan picks a routable LAN IPv4 address that a second machine on the
// same network can reach. It prefers the default-route interface and rejects
// Docker bridge addresses (172.16.0.0/12) that are unreachable from the host
// on Docker Desktop for macOS.
package lan

import (
	"fmt"
	"net"
	"strings"
)

// Input is the network state to choose from. Tests set it directly.
type Input struct {
	DefaultIface string
	Addrs        map[string][]string // iface -> CIDR addresses
}

// Choose returns the best routable IPv4 address. The order is:
//  1. First non-loopback, non-link-local, non-docker IPv4 on DefaultIface.
//  2. A usable IPv4 on a physical-looking interface (en*/eth*/wl*).
//  3. Any other usable IPv4, avoiding docker/bridge/vpn names.
//
// Loopback (127.0.0.0/8), link-local (169.254.0.0/16), and Docker bridge
// (172.16.0.0/12) are always rejected. RFC 1918 is allowed.
func Choose(in Input) (string, error) {
	if in.DefaultIface != "" {
		for _, cidr := range in.Addrs[in.DefaultIface] {
			if ip := usableIPv4(cidr); ip != "" {
				return ip, nil
			}
		}
	}
	var best string
	bestRank := 100
	for name, cidrs := range in.Addrs {
		rank := ifaceRank(name)
		if rank >= bestRank {
			continue
		}
		for _, cidr := range cidrs {
			if ip := usableIPv4(cidr); ip != "" {
				best = ip
				bestRank = rank
				break
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("no routable LAN address found; pass --lan-ip")
	}
	return best, nil
}

func ifaceRank(name string) int {
	n := strings.ToLower(name)
	switch {
	case n == "docker0" || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "veth") ||
		strings.HasPrefix(n, "virbr") || strings.HasPrefix(n, "cni") || strings.HasPrefix(n, "flannel") ||
		strings.HasPrefix(n, "tun") || strings.HasPrefix(n, "tap") || strings.HasPrefix(n, "utun") ||
		strings.HasPrefix(n, "wg"):
		return 50
	case strings.HasPrefix(n, "en") || strings.HasPrefix(n, "eth") || strings.HasPrefix(n, "wl") ||
		strings.HasPrefix(n, "em") || strings.HasPrefix(n, "re") || strings.HasPrefix(n, "bond"):
		return 0
	default:
		return 10
	}
}

// Detect finds a routable LAN IPv4 using the Go net package (macOS and Linux).
// It prefers the source address the OS would use for a default-route packet,
// then falls back to enumerating interfaces. Returns ("", err) when none is usable.
func Detect() (string, error) {
	if ip, err := udpSourceIPv4(); err == nil {
		if u := usableIPv4(ip); u != "" {
			return u, nil
		}
	}
	return Choose(Input{Addrs: interfaceCIDRs()})
}

// udpSourceIPv4 asks the kernel which local IPv4 it would use to reach the
// internet. UDP "connect" does not send packets; it only consults the routing table.
func udpSourceIPv4() (string, error) {
	conn, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "", fmt.Errorf("no local IPv4")
	}
	return addr.IP.String(), nil
}

func interfaceCIDRs() map[string][]string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make(map[string][]string)
	for _, ifi := range ifaces {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			out[ifi.Name] = append(out[ifi.Name], a.String())
		}
	}
	return out
}

// usableIPv4 parses a CIDR and returns the IP if it is acceptable.
func usableIPv4(cidr string) string {
	ipStr := cidr
	if strings.Contains(cidr, "/") {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			return ""
		}
		ipStr = ip.String()
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	ip = ip.To4()
	if ip == nil {
		return "" // IPv6
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ""
	}
	// Docker bridge 172.16.0.0/12 is unreachable from macOS host.
	if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
		return ""
	}
	return ip.String()
}
