package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/realclientip/realclientip-go"
)

// privateRanges is what "private" means in the trusted proxy list: the
// RFC 1918 ranges, link-local, and IPv6 unique local addresses.
var privateRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "fc00::/7", "fe80::/10"}

// loopbackRanges are always trusted.
var loopbackRanges = []string{"127.0.0.0/8", "::1/128"}

// ClientIPs resolves the client address of a request behind trusted proxies.
type ClientIPs struct {
	Trusted   []net.IPNet
	rightmost realclientip.RightmostTrustedRangeStrategy
}

// NewClientIPs parses a comma-separated list of trusted proxy addresses and
// ranges, where "private" stands for the private ranges.
func NewClientIPs(trusted string) (*ClientIPs, error) {
	ranges := append([]string{}, loopbackRanges...)

	for item := range strings.SplitSeq(trusted, ",") {
		switch item = strings.TrimSpace(item); {
		case item == "":
		case strings.EqualFold(item, "private"):
			ranges = append(ranges, privateRanges...)
		default:
			ranges = append(ranges, item)
		}
	}

	nets, err := realclientip.AddressesAndRangesToIPNets(ranges...)
	if err != nil {
		return nil, fmt.Errorf("reading the trusted proxies: %w", err)
	}

	rightmost, err := realclientip.NewRightmostTrustedRangeStrategy("X-Forwarded-For", nets)
	if err != nil {
		return nil, err
	}

	return &ClientIPs{Trusted: nets, rightmost: rightmost}, nil
}

// Of returns a request's client address. When the connecting address is a
// trusted proxy, that's the rightmost X-Forwarded-For hop that isn't trusted.
// Otherwise it's the connecting address, and the forwarding headers, which
// the client could have written, are ignored.
func (c *ClientIPs) Of(r *http.Request) string {
	peer := realclientip.RemoteAddrStrategy{}.ClientIP(nil, r.RemoteAddr)
	if !c.IsTrusted(peer) {
		return peer
	}

	if ip := c.rightmost.ClientIP(r.Header, r.RemoteAddr); ip != "" {
		return ip
	}

	return peer
}

// IsTrusted reports whether an address is in a trusted range.
func (c *ClientIPs) IsTrusted(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}

	for _, n := range c.Trusted {
		if n.Contains(ip) {
			return true
		}
	}

	return false
}
