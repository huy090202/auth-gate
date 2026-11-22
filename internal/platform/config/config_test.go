package config

import "testing"

// Why: a role booting with missing wiring would fail late (first request) or,
// worse, fall back to a local signer on AWS. Load must refuse at cold start.
func TestLoadRefusesIncompleteRoles(t *testing.T) {
	verifyOK := map[string]string{
		"ROLE": "verify", "TABLE_NAME": "t", "BASE_URL": "https://x", "ORIGIN_VERIFY_PARAM": "/p", "KMS_KEY_ID": "k",
	}
	adminOK := map[string]string{
		"ROLE": "admin", "TABLE_NAME": "t", "ORIGIN_VERIFY_PARAM": "/p", "ADMIN_KEY_PARAM": "/a", "PORTAL_KEY_PARAM": "/k",
	}
	without := func(base map[string]string, drop ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for _, d := range drop {
			delete(m, d)
		}
		return m
	}
	cases := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"verify complete", verifyOK, true},
		{"verify without kms and without local signer", without(verifyOK, "KMS_KEY_ID"), false},
		{"verify without origin param", without(verifyOK, "ORIGIN_VERIFY_PARAM"), false},
		{"verify without base url", without(verifyOK, "BASE_URL"), false},
		{"admin complete", adminOK, true},
		{"admin without admin key param", without(adminOK, "ADMIN_KEY_PARAM"), false},
		{"admin without portal key param", without(adminOK, "PORTAL_KEY_PARAM"), false},
		{"jwks without key ids", map[string]string{"ROLE": "jwks"}, false},
		{"unknown role", map[string]string{"ROLE": "nope", "TABLE_NAME": "t"}, false},
		{"dev with only table", map[string]string{"ROLE": "dev", "TABLE_NAME": "t"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(func(k string) string { return tc.env[k] })
			if (err == nil) != tc.ok {
				t.Fatalf("Load err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
