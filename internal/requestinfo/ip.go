package requestinfo

import (
	"net"
	"net/http"
	"net/netip"
)

// The permanent deployment only publishes its HTTP port on loopback. Trust
// Cloudflare's client address solely across that local/private proxy boundary.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err == nil && (peer.IsLoopback() || peer.IsPrivate()) {
		if forwarded, err := netip.ParseAddr(r.Header.Get("CF-Connecting-IP")); err == nil {
			return forwarded.String()
		}
	}
	return host
}
