package console

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// NormalizeUsername lower-cases s and accepts 3 to 32 of a-z, 0-9, "_", "." and "-".
func NormalizeUsername(s string) (string, error) {
	s = strings.ToLower(s)
	if len(s) < 3 || len(s) > 32 {
		return "", errors.New("username must be 3 to 32 characters")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return "", errors.New("username may use a-z, 0-9, _ . -")
		}
	}
	return s, nil
}

// NewTempPassword returns 20 random bytes as base64url, for a one-time password.
func NewTempPassword() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic("console: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
