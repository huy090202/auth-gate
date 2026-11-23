package reset

import "testing"

func TestDecide(t *testing.T) {
	k := Keep{Clients: map[string]bool{"console": true, "portal": true, "app": true}, Console: "console"}
	rows := []struct {
		name     string
		table    Table
		pk, sk   string
		clientID string
		label    string
		del      bool
	}{
		{"client kept", AuthGate, "C#portal", "META", "", "client", false},
		{"client gone", AuthGate, "C#old", "META", "", "client", true},
		{"index kept", AuthGate, "CLIENTS", "C#app", "", "client-index", false},
		{"index gone", AuthGate, "CLIENTS", "C#old", "", "client-index", true},
		{"key kept", AuthGate, "K#x", "META", "portal", "key", false},
		{"key other", AuthGate, "K#x", "META", "old", "key", true},
		{"key no client", AuthGate, "K#x", "META", "", "key", true},
		{"factor console", AuthGate, "S#console#a1", "PIN", "", "factor", false},
		{"factor portal", AuthGate, "S#portal#u1", "PIN", "", "factor", true},
		{"factor malformed", AuthGate, "S#console", "PIN", "", "unknown", false},
		{"rate kept", AuthGate, "RL#app", "META", "", "rate-bucket", false},
		{"rate gone", AuthGate, "RL#old", "META", "", "rate-bucket", true},
		{"challenge kept", AuthGate, "CH#portal#c1", "META", "", "challenge", false},
		{"challenge gone", AuthGate, "CH#old#c1", "META", "", "challenge", true},
		{"registration kept", AuthGate, "R#app#r1", "META", "", "registration", false},
		{"registration gone", AuthGate, "R#old#r1", "META", "", "registration", true},
		{"owner", AuthGate, "OWNER#t1", "META", "", "owner", true},
		{"unknown", AuthGate, "ZZ#1", "META", "", "unknown", false},
		{"admins", Console, "ADMINS", "A#huy", "", "admin", false},
		{"session", Console, "SES#1", "x", "", "session", true},
		{"jti", Console, "JTI#1", "x", "", "used-token", true},
		{"console rate", Console, "RL#1", "x", "", "rate-bucket", true},
		{"audit", Console, "AUDIT#2026-11", "x", "", "audit", true},
		{"console unknown", Console, "ZZ#1", "x", "", "unknown", false},
		{"portal user", Portal, "USER#u", "META", "", "USER", true},
		{"portal tenants", Portal, "TENANTS", "t1", "", "TENANTS", true},
		{"authapp", AuthApp, "NOTE#n1", "x", "", "NOTE", true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			label, del := Decide(r.table, r.pk, r.sk, r.clientID, k)
			if label != r.label || del != r.del {
				t.Errorf("Decide(%s,%q,%q,%q) = (%q,%v), want (%q,%v)", r.table, r.pk, r.sk, r.clientID, label, del, r.label, r.del)
			}
		})
	}
}
