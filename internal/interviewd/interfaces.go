package interviewd

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

type rankedIP struct {
	ip   netip.Addr
	rank int
	name string
}

func servingAddresses(port int, id string) []string {
	ips := collectIPv4s()
	seen := map[string]bool{}
	portString := strconv.Itoa(port)

	urls := make([]string, 0, len(ips)+1)
	for _, item := range ips {
		ip := item.ip.String()
		if seen[ip] {
			continue
		}

		seen[ip] = true
		urls = append(urls, fmt.Sprintf("http://%s/%s", net.JoinHostPort(ip, portString), id))
	}

	if !seen["127.0.0.1"] {
		urls = append(urls, fmt.Sprintf("http://%s/%s", net.JoinHostPort("127.0.0.1", portString), id))
	}

	urls = append(urls, fmt.Sprintf("http://%s/%s", net.JoinHostPort("0.0.0.0", portString), id))

	return urls
}

func collectIPv4s() []rankedIP {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var result []rankedIP

	for _, ifi := range interfaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var raw net.IP

			switch v := addr.(type) {
			case *net.IPNet:
				raw = v.IP.To4()
			case *net.IPAddr:
				raw = v.IP.To4()
			}

			ip, ok := netip.AddrFromSlice(raw)
			if !ok || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}

			result = append(result, rankedIP{ip: ip, rank: rankIP(ip, ifi), name: ifi.Name})
		}
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].rank != result[j].rank {
			return result[i].rank < result[j].rank
		}

		if result[i].name != result[j].name {
			return result[i].name < result[j].name
		}

		return result[i].ip.Compare(result[j].ip) < 0
	})

	return result
}

func rankIP(ip netip.Addr, ifi net.Interface) int {
	if ip.IsLoopback() {
		return 5
	}

	if isVPNInterface(ifi) {
		return 2
	}

	if netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10).Contains(ip) {
		return 3
	}

	if ip.IsPrivate() {
		return 4
	}

	return 1
}

func isVPNInterface(ifi net.Interface) bool {
	name := strings.ToLower(ifi.Name)
	if ifi.Flags&net.FlagPointToPoint != 0 {
		return true
	}

	for _, prefix := range []string{"tun", "tap", "utun", "wg", "tailscale", "zt", "nebula"} {
		if strings.HasPrefix(name, prefix) || strings.Contains(name, prefix) {
			return true
		}
	}

	return false
}
