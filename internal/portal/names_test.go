package portal

import (
	"strings"
	"testing"
)

// Why: a URL in a name would travel into invite emails and the Console as a phishing link.
func TestHasURL(t *testing.T) {
	for v, want := range map[string]bool{
		"Acme": false, "Mr. Smith": false, "J.R.R. Tolkien": false, "Nguyễn Văn A": false,
		"acme.io": true, "see acme.io/x": true, "http://x": true, "WWW.EVIL": true, "evil.com?a": true,
		"visit acme.io!": true, "acme.io,": true, "(acme.io)": true, "acme.io.": true,
		"acme.io\u00a0x": true, "acme.io\u200bx": true, "dụ.com": true,
	} {
		if got := hasURL(v); got != want {
			t.Errorf("hasURL(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestValidOrgName(t *testing.T) {
	for _, c := range []struct {
		in, out string
		ok      bool
	}{
		{"A", "", false},
		{"Acme", "Acme", true},
		{strings.Repeat("é", 65), "", false},
		{strings.Repeat("é", 64), strings.Repeat("é", 64), true},
		{"acme.io", "", false},
		{" Acme ", "Acme", true},
		{"Ac\tme", "", false},
	} {
		if out, ok := validOrgName(c.in); out != c.out || ok != c.ok {
			t.Errorf("validOrgName(%q) = %q, %v", c.in, out, ok)
		}
	}
}

// Why: a display name saved before the URL rule must not reach an invite email.
func TestInviterLabel(t *testing.T) {
	for in, want := range map[string]string{"Ann": "Ann", "": "", "evil.com": ""} {
		if got := inviterLabel(in); got != want {
			t.Errorf("inviterLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
