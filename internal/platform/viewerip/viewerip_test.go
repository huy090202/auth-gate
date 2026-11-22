package viewerip

import (
	"net/http"
	"testing"
)

// Why: the viewer address is the rate-limit key; a wrong parse would merge or split buckets.
func TestFromHeader(t *testing.T) {
	cases := map[string]string{
		"198.51.100.10:46532":   "198.51.100.10",
		"[2001:db8::1]:46532":   "2001:db8::1",
		"2001:db8::1:46532":     "2001:db8::1",
		"":                      "",
		"garbage":               "",
		"999.1.1.1:80":          "",
		"198.51.100.10:notport": "",
	}
	for in, want := range cases {
		h := http.Header{}
		if in != "" {
			h.Set("CloudFront-Viewer-Address", in)
		}
		if got := FromHeader(h); got != want {
			t.Errorf("FromHeader(%q) = %q, want %q", in, got, want)
		}
	}
	// A viewer-supplied value passed along with CloudFront's own could come first
	// and pick the bucket, so two values count as none.
	h := http.Header{}
	h.Add("CloudFront-Viewer-Address", "203.0.113.7:1")
	h.Add("CloudFront-Viewer-Address", "198.51.100.10:46532")
	if got := FromHeader(h); got != "" {
		t.Errorf("FromHeader(two values) = %q, want \"\"", got)
	}
}
