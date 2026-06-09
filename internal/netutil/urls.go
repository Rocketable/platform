// Package netutil contains shared network address discovery helpers.
package netutil

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// URLCandidatesOptions configures CandidateHTTPURLs.
type URLCandidatesOptions struct {
	Addr               string
	BaseURL            string
	Path               string
	IncludePublic      bool
	IncludeUnspecified bool
}

// CandidateHTTPURLs returns human-facing HTTP URLs in preference order.
func CandidateHTTPURLs(opts URLCandidatesOptions) []string {
	port := portFromAddr(opts.Addr)
	path := normalizeURLPath(opts.Path)
	seen := map[string]bool{}
	var urls []string

	if opts.BaseURL != "" {
		url := strings.TrimRight(opts.BaseURL, "/") + path
		urls = append(urls, url)
		seen[url] = true
	}

	for _, item := range collectAddrs(opts.IncludePublic) {
		url := fmt.Sprintf("http://%s%s", net.JoinHostPort(item.addr.String(), port), path)
		if seen[url] {
			continue
		}

		seen[url] = true
		urls = append(urls, url)
	}

	loopback := fmt.Sprintf("http://%s%s", net.JoinHostPort("127.0.0.1", port), path)
	if !seen[loopback] {
		seen[loopback] = true
		urls = append(urls, loopback)
	}

	if opts.IncludeUnspecified {
		unspecified := fmt.Sprintf("http://%s%s", net.JoinHostPort("0.0.0.0", port), path)
		if !seen[unspecified] {
			urls = append(urls, unspecified)
		}
	}

	return urls
}

type rankedAddr struct {
	addr netip.Addr
	rank int
	name string
}

func normalizeURLPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}

	return path
}

func portFromAddr(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err == nil && port != "" {
		return port
	}

	if strings.HasPrefix(addr, ":") && len(addr) > 1 {
		return strings.TrimPrefix(addr, ":")
	}

	return strconv.Itoa(8797)
}

func collectAddrs(includePublic bool) []rankedAddr {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var result []rankedAddr
	for _, ifi := range interfaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ip, ok := addrFromInterfaceAddr(addr)
			if !ok || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
				continue
			}

			rank := rankAddr(ip, ifi, includePublic)
			if rank < 0 {
				continue
			}

			result = append(result, rankedAddr{addr: ip, rank: rank, name: ifi.Name})
		}
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].rank != result[j].rank {
			return result[i].rank < result[j].rank
		}

		if result[i].name != result[j].name {
			return result[i].name < result[j].name
		}

		return result[i].addr.Compare(result[j].addr) < 0
	})

	seen := map[netip.Addr]bool{}
	unique := result[:0]
	for _, item := range result {
		if seen[item.addr] {
			continue
		}

		seen[item.addr] = true
		unique = append(unique, item)
	}

	return unique
}

func addrFromInterfaceAddr(addr net.Addr) (netip.Addr, bool) {
	switch v := addr.(type) {
	case *net.IPNet:
		return netip.AddrFromSlice(v.IP)
	case *net.IPAddr:
		return netip.AddrFromSlice(v.IP)
	default:
		return netip.Addr{}, false
	}
}

func rankAddr(ip netip.Addr, ifi net.Interface, includePublic bool) int {
	if ip.IsLoopback() {
		return 90
	}

	if isVPNInterface(ifi) && (ip.IsPrivate() || isCGNAT(ip) || includePublic) {
		return 10
	}

	if ip.Is4() && isCGNAT(ip) {
		return 20
	}

	if ip.Is4() && ip.IsPrivate() {
		return 30
	}

	if !ip.Is4() && ip.IsPrivate() {
		return 50
	}

	if includePublic {
		return 70
	}

	return -1
}

func isCGNAT(ip netip.Addr) bool {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10).Contains(ip)
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
