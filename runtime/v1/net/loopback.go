package net

import (
	"strconv"
	"strings"
)

// LiteralLoopback reports whether host is one of the two literal hosts
// admitted by TestLoopback. It never resolves a name or normalizes an IP.
//
//ergo:pure
func LiteralLoopback(host string) bool { return host == "127.0.0.1" || host == "::1" }

// LoopbackAddress admits only a literal loopback TCP address and a decimal
// port. It uses no system service-name lookup.
//
//ergo:pure
//ergo:wait none
func LoopbackAddress(addr string) bool {
	host, port, found := strings.Cut(addr, ":")
	if strings.HasPrefix(addr, "[") {
		host, port, found = strings.Cut(addr[1:], "]:")
		if host != "::1" {
			return false
		}
	} else if host != "127.0.0.1" {
		return false
	}
	if !found || port == "" {
		return false
	}
	for i := range len(port) {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(port, 10, 16)
	return err == nil
}
