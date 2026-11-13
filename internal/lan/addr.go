// Package lan picks a routable LAN IPv4 address that a second machine on the
// same network can reach. It prefers the default-route interface and rejects
// Docker bridge addresses (172.16.0.0/12) that are unreachable from the host
// on Docker Desktop for macOS.
package lan

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// Input is the network state to choose from. Tests set it directly.
type Input struct {
	DefaultIface string
	Addrs        map[string][]string // iface -> CIDR addresses
}

// Choose returns the best routable IPv4 address. The order is:
//  1. First non-loopback, non-link-local, non-docker IPv4 on DefaultIface.
//  2. First such IPv4 on any other interface.
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
	for _, iface := range in.Addrs {
		for _, cidr := range iface {
			if ip := usableIPv4(cidr); ip != "" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no routable LAN address found; pass --lan-ip")
}

// Detect shells out to find the default-route interface and its IPv4.
// Returns ("", err) when it cannot determine one.
func Detect() (string, error) {
	iface := defaultInterface()
	if iface == "" {
		return "", fmt.Errorf("cannot determine default interface")
	}
	addrs := map[string][]string{
		iface: {ipv4Of(iface)},
	}
	for _, name := range allInterfaces() {
		if _, ok := addrs[name]; !ok {
			addrs[name] = []string{ipv4Of(name)}
		}
	}
	return Choose(Input{DefaultIface: iface, Addrs: addrs})
}

// defaultInterface runs `route -n get default` (macOS) or `ip route` (Linux).
func defaultInterface() string {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "interface:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					return fields[len(fields)-1]
				}
			}
		}
	}
	out, err = exec.Command("ip", "route", "show", "default").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "dev ") {
				fields := strings.Fields(line)
				for i, f := range fields {
					if f == "dev" && i+1 < len(fields) {
						return fields[i+1]
					}
				}
			}
		}
	}
	return ""
}

// ipv4Of returns the first IPv4 CIDR on iface.
func ipv4Of(iface string) string {
	out, err := exec.Command("ipconfig", "getifaddr", iface).Output()
	if err == nil {
		return strings.TrimSpace(string(out)) + "/24"
	}
	out, err = exec.Command("ip", "-4", "-o", "addr", "show", "dev", iface).Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "inet" && i+1 < len(fields) {
					return fields[i+1]
				}
			}
		}
	}
	return ""
}

// allInterfaces lists interface names.
func allInterfaces() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var names []string
	for _, ifc := range ifaces {
		names = append(names, ifc.Name)
	}
	return names
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
