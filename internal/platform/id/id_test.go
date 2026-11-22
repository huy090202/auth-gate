package id

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRejectsHashEmptyAndLong(t *testing.T) {
	for _, s := range []string{"a#b", "", strings.Repeat("x", 257)} {
		if err := Validate(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%q) = %v, want ErrInvalid", s, err)
		}
	}
	if err := Validate("user_1"); err != nil {
		t.Errorf("Validate(user_1) = %v, want nil", err)
	}
}

// Why: a tenant id is a partition key, so one tenant must have exactly one
// spelling; ParseStrict alone accepts a lowercase copy of the same ULID.
func TestValidateULIDAcceptsOnlyCanonical(t *testing.T) {
	good := New()
	for _, s := range []string{"", "nope", strings.ToLower(good), "8" + good[1:], good + "A", "T#" + good} {
		if err := ValidateULID(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateULID(%q) = %v, want ErrInvalid", s, err)
		}
	}
	if err := ValidateULID(good); err != nil {
		t.Errorf("ValidateULID(%q) = %v, want nil", good, err)
	}
}
