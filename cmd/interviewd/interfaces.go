package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

type rankedIP struct {
	ip   net.IP
	rank int
	name string
}

func servingAddresses(port int, id string) []string {
	ips := collectIPv4s()
	seen := map[string]bool{}
	urls := make([]string, 0, len(ips)+1)
	for _, item := range ips {
		ip := item.ip.String()
		if seen[ip] {
			continue
		}
		seen[ip] = true
		urls = append(urls, fmt.Sprintf("http://%s:%d/%s", ip, port, id))
	}
	if !seen["127.0.0.1"] {
		urls = append(urls, fmt.Sprintf("http://127.0.0.1:%d/%s", port, id))
	}
	urls = append(urls, fmt.Sprintf("http://0.0.0.0:%d/%s", port, id))
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
			ip := ipFromAddr(addr)
			if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
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
		return bytesCompare(result[i].ip, result[j].ip) < 0
	})
	return result
}

func ipFromAddr(addr net.Addr) net.IP {
	var ip net.IP
	switch v := addr.(type) {
	case *net.IPNet:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	}
	if ip == nil {
		return nil
	}
	return ip.To4()
}

func rankIP(ip net.IP, ifi net.Interface) int {
	if ip.IsLoopback() {
		return 5
	}
	if isVPNInterface(ifi) {
		return 2
	}
	if isCGNAT(ip) {
		return 3
	}
	if isPrivateIPv4(ip) {
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

func isCGNAT(ip net.IP) bool {
	return ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

func isPrivateIPv4(ip net.IP) bool {
	return ip[0] == 10 || ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 || ip[0] == 192 && ip[1] == 168
}

func bytesCompare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return len(a) - len(b)
}
