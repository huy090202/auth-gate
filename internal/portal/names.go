package portal

import (
	"strings"
	"unicode"
	"unicode/utf8"

	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

const (
	minOrgName = 2
	maxOrgName = 64
)

// hasURL reports whether s looks like a URL or a host name. Names that do are
// refused, so a name can never carry a link into an email or a page.
func hasURL(s string) bool { return pmail.HasURL(s) }

// validOrgName trims raw and accepts 2 to 64 runes, no control characters, no URL.
func validOrgName(raw string) (string, bool) {
	n := strings.TrimSpace(raw)
	if c := utf8.RuneCountInString(n); c < minOrgName || c > maxOrgName || hasURL(n) {
		return "", false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return n, true
}

// inviterLabel is the display name to put in an invite, or "" when it is empty
// or was saved before names were checked and holds a URL; the mail then says
// "A member of <org>".
func inviterLabel(displayName string) string {
	if displayName == "" || hasURL(displayName) {
		return ""
	}
	return displayName
}
