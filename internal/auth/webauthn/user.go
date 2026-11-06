package webauthn

import (
	gowa "github.com/go-webauthn/webauthn/webauthn"

	"github.com/huy090202/auth-gate/internal/auth/factor"
)

// MaxUserIDLen is the WebAuthn limit for user.id, in bytes. The subject ID is the user handle.
const MaxUserIDLen = 64

type user struct {
	id, name string
	creds    []gowa.Credential
}

// NewUser returns the go-webauthn user for a subject. The user handle is the subject ID.
func NewUser(subjectID, displayName string, creds []factor.Credential) gowa.User {
	if displayName == "" {
		displayName = subjectID
	}
	u := &user{id: subjectID, name: displayName, creds: make([]gowa.Credential, 0, len(creds))}
	for _, c := range creds {
		u.creds = append(u.creds, FromCredential(c))
	}
	return u
}

func (u *user) WebAuthnID() []byte                     { return []byte(u.id) }
func (u *user) WebAuthnName() string                   { return u.id }
func (u *user) WebAuthnDisplayName() string            { return u.name }
func (u *user) WebAuthnCredentials() []gowa.Credential { return u.creds }
