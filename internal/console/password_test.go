package console

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Why: sign-in depends on a hash verifying with its own password and only that one.
func TestPasswordRoundTripAndWrongPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected hash format %q", h)
	}
	if !VerifyPassword("correct horse battery", h) {
		t.Fatal("right password rejected")
	}
	if VerifyPassword("correct horse batterz", h) {
		t.Fatal("wrong password accepted")
	}
}

// Why: an unknown username is checked against dummyHash so the response time does
// not reveal which usernames exist. That only holds while its cost matches real hashes.
func TestDummyHashUsesCurrentParams(t *testing.T) {
	real, err := HashPassword("whatever-password")
	if err != nil {
		t.Fatal(err)
	}
	params := func(s string) string {
		var m, tt, p int
		parts := strings.Split(s, "$")
		if len(parts) != 6 {
			t.Fatalf("hash %q has %d parts", s, len(parts))
		}
		if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &tt, &p); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d/%d/%d", m, tt, p)
	}
	if params(dummyHash) != params(real) {
		t.Fatalf("dummyHash params %s, HashPassword writes %s", params(dummyHash), params(real))
	}
	if VerifyPassword("anything", dummyHash) {
		t.Fatal("dummyHash must not verify a guess")
	}
}

// Why: the stored string comes from a table; a damaged one must deny, not crash the Lambda.
func TestVerifyPasswordMalformedIsFalse(t *testing.T) {
	for _, s := range []string{
		"", "garbage", "$argon2id$", "$argon2id$v=19$m=65536,t=3,p=1$$",
		"$argon2id$v=19$m=x,t=3,p=1$AAAA$AAAA", "$argon2id$v=19$m=0,t=0,p=0$AAAA$AAAA",
		"$argon2id$v=19$m=65536,t=3,p=1$!!!$!!!", "$argon2i$v=19$m=65536,t=3,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=65536,t=3,p=300$AAAA$AAAA",
	} {
		if VerifyPassword("password-12345", s) {
			t.Fatalf("%q verified", s)
		}
	}
}

// Why: a megabyte password must not make the server hash a megabyte, and must never pass.
func TestOverlongPasswordIsFalseAndBounded(t *testing.T) {
	h, err := HashPassword(strings.Repeat("a", 128))
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 128) + strings.Repeat("b", 1<<20)
	start := time.Now()
	if VerifyPassword(long, h) {
		t.Fatal("overlong password accepted, though its first 128 bytes match")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("overlong verify took %v", d)
	}
}
