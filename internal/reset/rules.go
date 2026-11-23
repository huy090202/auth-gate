// Package reset holds the keep-or-delete rules for resetting stg's user data.
package reset

import "strings"

// Table names one of the four DynamoDB tables the reset covers.
type Table string

const (
	AuthGate Table = "authgate"
	Console  Table = "console"
	Portal   Table = "portal"
	AuthApp  Table = "authapp"
)

// Keep is the set of clients that survive a reset.
type Keep struct {
	// Clients holds the kept client ids (Console, Portal and auth-app).
	Clients map[string]bool
	// Console is the Console's client id, whose factors are kept; it is also in Clients.
	Console string
}

// Decide returns the report label for an item and whether to delete it.
// clientID is the item's clientId attribute; only K# items carry it.
// Items of unknown shape are labelled "unknown" and kept.
func Decide(t Table, pk, sk, clientID string, k Keep) (label string, del bool) {
	switch t {
	case AuthGate:
		return decideAuthGate(pk, sk, clientID, k)
	case Console:
		return decideConsole(pk)
	case Portal, AuthApp:
		return prefix(pk), true
	}
	return "unknown", false
}

func decideAuthGate(pk, sk, clientID string, k Keep) (string, bool) {
	switch {
	case strings.HasPrefix(pk, "C#") && sk == "META":
		return "client", !k.Clients[strings.TrimPrefix(pk, "C#")]
	case pk == "CLIENTS" && strings.HasPrefix(sk, "C#"):
		return "client-index", !k.Clients[strings.TrimPrefix(sk, "C#")]
	case strings.HasPrefix(pk, "K#") && sk == "META":
		return "key", !k.Clients[clientID]
	case strings.HasPrefix(pk, "S#"):
		cid, _, ok := strings.Cut(strings.TrimPrefix(pk, "S#"), "#")
		if !ok {
			return "unknown", false
		}
		return "factor", cid != k.Console
	case strings.HasPrefix(pk, "RL#") && sk == "META":
		return "rate-bucket", !k.Clients[strings.TrimPrefix(pk, "RL#")]
	case strings.HasPrefix(pk, "CH#"):
		return cidItem("challenge", strings.TrimPrefix(pk, "CH#"), k)
	case strings.HasPrefix(pk, "R#"):
		return cidItem("registration", strings.TrimPrefix(pk, "R#"), k)
	case strings.HasPrefix(pk, "OWNER#"):
		return "owner", true
	}
	return "unknown", false
}

// cidItem handles <cid>#... keys; a missing second part is unknown and kept.
func cidItem(label, rest string, k Keep) (string, bool) {
	cid, _, ok := strings.Cut(rest, "#")
	if !ok {
		return "unknown", false
	}
	return label, !k.Clients[cid]
}

func decideConsole(pk string) (string, bool) {
	switch {
	case pk == "ADMINS":
		return "admin", false
	case strings.HasPrefix(pk, "SES#"):
		return "session", true
	case strings.HasPrefix(pk, "JTI#"):
		return "used-token", true
	case strings.HasPrefix(pk, "RL#"):
		return "rate-bucket", true
	case strings.HasPrefix(pk, "AUDIT#"):
		return "audit", true
	}
	return "unknown", false
}

// prefix is the pk up to its first '#', or the whole pk when it has none.
func prefix(pk string) string {
	p, _, _ := strings.Cut(pk, "#")
	return p
}
