package console

import (
	"net"
	"net/http"
	"strings"
)

// ViewerIP returns the caller's address from the CloudFront-Viewer-Address
// header, which CloudFront sets itself, unlike X-Forwarded-For. It accepts
// "ip:port", "[ipv6]:port" and the unbracketed "ipv6:port" CloudFront sends,
// and returns "" when the header is absent or not an address.
func ViewerIP(h http.Header) string {
	v := strings.TrimSpace(h.Get("CloudFront-Viewer-Address"))
	if v == "" {
		return ""
	}
	host := v
	if i := strings.LastIndexByte(v, ':'); i >= 0 {
		host = strings.TrimSuffix(strings.TrimPrefix(v[:i], "["), "]")
		if p := v[i+1:]; p == "" || strings.Trim(p, "0123456789") != "" {
			return ""
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	return ip.String()
}
