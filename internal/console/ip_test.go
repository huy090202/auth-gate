package console

import (
	"net/http"
	"testing"
)

// Why: the viewer address is the rate-limit key; a wrong parse would merge or split buckets.
func TestViewerIP(t *testing.T) {
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
		if got := ViewerIP(h); got != want {
			t.Errorf("ViewerIP(%q) = %q, want %q", in, got, want)
		}
	}
}
