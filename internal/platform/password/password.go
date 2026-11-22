// Package password hashes and checks passwords with argon2id, for the Console and the Portal.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The cost matches internal/auth/pin; it is copied, not imported.
const (
	argonMemory  = 65536
	argonTime    = 3
	argonThreads = 1
	saltLen      = 16
	keyLen       = 32
	maxPassword  = 128
	minPassword  = 12
)

// DummyHash is a real hash of a throwaway string, made once by Hash and
// pasted here so a cold start does not pay for it. Sign-in verifies against it
// when the username is unknown, so both paths cost the same.
const DummyHash = "$argon2id$v=19$m=65536,t=3,p=1$9+bdaMbAysasOKfDOQg+jA$RDu8Uy217Uqe2ZrbLHl+Fat1Fgk0DYsJBU+WuYyxdMA"

var b64 = base64.RawStdEncoding

// Hash returns "$argon2id$v=19$m=65536,t=3,p=1$<salt>$<hash>" in raw base64.
func Hash(p string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify checks p against a stored hash using the record's own
// parameters, in constant time. A password over 128 bytes hashes only its first
// 128 bytes, so the work stays bounded and the timing like any other, and is
// then refused. A malformed record is false.
func Verify(p, stored string) bool {
	over := len(p) > maxPassword
	if over {
		p = p[:maxPassword]
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m, t, th uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return false
	}
	if m == 0 || t == 0 || th == 0 || th > 255 {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) != keyLen {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, t, m, uint8(th), keyLen)
	return subtle.ConstantTimeCompare(got, want) == 1 && !over
}

// Validate accepts 12 to 128 bytes.
func Validate(p string) error {
	if len(p) < minPassword || len(p) > maxPassword {
		return fmt.Errorf("password must be %d to %d bytes", minPassword, maxPassword)
	}
	return nil
}
