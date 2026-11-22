package password

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Why: sign-in depends on a hash verifying with its own password and only that one.
func TestPasswordRoundTripAndWrongPassword(t *testing.T) {
	h, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected hash format %q", h)
	}
	if !Verify("correct horse battery", h) {
		t.Fatal("right password rejected")
	}
	if Verify("correct horse batterz", h) {
		t.Fatal("wrong password accepted")
	}
}

// Why: an unknown username is checked against DummyHash so the response time does
// not reveal which usernames exist. That only holds while its cost matches real hashes.
func TestDummyHashUsesCurrentParams(t *testing.T) {
	real, err := Hash("whatever-password")
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
	if params(DummyHash) != params(real) {
		t.Fatalf("DummyHash params %s, Hash writes %s", params(DummyHash), params(real))
	}
	if Verify("anything", DummyHash) {
		t.Fatal("DummyHash must not verify a guess")
	}
}

// Why: the stored string comes from a table; a damaged one must deny, not crash the Lambda.
func TestVerifyMalformedIsFalse(t *testing.T) {
	for _, s := range []string{
		"", "garbage", "$argon2id$", "$argon2id$v=19$m=65536,t=3,p=1$$",
		"$argon2id$v=19$m=x,t=3,p=1$AAAA$AAAA", "$argon2id$v=19$m=0,t=0,p=0$AAAA$AAAA",
		"$argon2id$v=19$m=65536,t=3,p=1$!!!$!!!", "$argon2i$v=19$m=65536,t=3,p=1$AAAA$AAAA",
		"$argon2id$v=19$m=65536,t=3,p=300$AAAA$AAAA",
	} {
		if Verify("password-12345", s) {
			t.Fatalf("%q verified", s)
		}
	}
}

// Why: a megabyte password must not make the server hash a megabyte, and must never pass.
func TestOverlongPasswordIsFalseAndBounded(t *testing.T) {
	h, err := Hash(strings.Repeat("a", 128))
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 128) + strings.Repeat("b", 1<<20)
	start := time.Now()
	if Verify(long, h) {
		t.Fatal("overlong password accepted, though its first 128 bytes match")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("overlong verify took %v", d)
	}
}
