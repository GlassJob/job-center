package netutil

import (
	"net"
	"strings"
)

// IPAllowed NULL/空列表=不限制；条目可为单 IP 或 CIDR。
func IPAllowed(allowed *[]string, remote string) bool {
	if allowed == nil {
		return true
	}
	list := *allowed
	if len(list) == 0 {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(remote))
	if ip == nil {
		// 可能带端口
		host, _, err := net.SplitHostPort(remote)
		if err != nil {
			return false
		}
		ip = net.ParseIP(host)
		if ip == nil {
			return false
		}
	}
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			_, cidr, err := net.ParseCIDR(raw)
			if err != nil {
				continue
			}
			if cidr.Contains(ip) {
				return true
			}
			continue
		}
		if other := net.ParseIP(raw); other != nil && other.Equal(ip) {
			return true
		}
	}
	return false
}
