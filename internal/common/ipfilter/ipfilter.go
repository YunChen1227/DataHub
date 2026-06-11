// Package ipfilter matches a source IP against a whitelist of single IPs and
// CIDR ranges (DESIGN §16.4).
package ipfilter

import (
	"net"
	"strings"
)

// Allowed reports whether ip is permitted by the whitelist. An empty whitelist
// means "no restriction" (allow all).
func Allowed(ip string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, entry := range whitelist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, cidr, err := net.ParseCIDR(entry); err == nil && cidr.Contains(parsed) {
				return true
			}
			continue
		}
		if e := net.ParseIP(entry); e != nil && e.Equal(parsed) {
			return true
		}
	}
	return false
}

// ClientIP extracts the source IP from X-Forwarded-For (first hop) or falls back
// to the host part of remoteAddr.
func ClientIP(xff, remoteAddr string) string {
	if xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
