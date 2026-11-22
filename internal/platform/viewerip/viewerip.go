// Package viewerip reads the caller's address behind CloudFront.
package viewerip

import (
	"net"
	"net/http"
	"strings"
)

// FromHeader returns the caller's address from the CloudFront-Viewer-Address
// header, which CloudFront sets itself, unlike X-Forwarded-For. It accepts
// "ip:port", "[ipv6]:port" and the unbracketed "ipv6:port" CloudFront sends,
// and returns "" when the header is absent or not an address. More than one value
// (the Lambda adapter splits a header on commas) is also "": a viewer-supplied value
// passed along with CloudFront's could come first, so such a caller gets the shared bucket.
func FromHeader(h http.Header) string {
	vs := h.Values("CloudFront-Viewer-Address")
	if len(vs) != 1 {
		return ""
	}
	v := strings.TrimSpace(vs[0])
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
