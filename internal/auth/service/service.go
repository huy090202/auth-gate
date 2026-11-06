// Package service implements the verify use cases: PIN enrollment, factor
// listing, challenges and assertion tokens.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	gowa "github.com/go-webauthn/webauthn/webauthn"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/pin"
	"github.com/huy090202/auth-gate/internal/auth/token"
	"github.com/huy090202/auth-gate/internal/auth/webauthn"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

const (
	challengeTTL = 5 * time.Minute
	// webauthnMaxAttempts bounds assertions per challenge; passkeys are not guessable.
	webauthnMaxAttempts = 3
)

// Service wires the stores and the signer behind the verify API.
type Service struct {
	Clients    *client.Service
	Factors    factor.Store
	Challenges challenge.Store
	Signer     token.Signer
	Issuer     string
	Now        func() time.Time
}

// Token is an issued assertion token.
type Token struct {
	Token     string
	ExpiresAt time.Time
	Factor    challenge.Factor
}

// FactorsView lists what a subject has enrolled. WebAuthn is never nil.
type FactorsView struct {
	PIN      bool             `json:"pin"`
	WebAuthn []CredentialView `json:"webauthn"`
}

// CredentialView is the public view of a passkey. LastUsedAt is null until first use.
type CredentialView struct {
	CredentialID string     `json:"credentialId"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func validateIDs(ids ...string) error {
	for _, v := range ids {
		if err := id.Validate(v); err != nil {
			return err
		}
	}
	return nil
}

// EnrollPIN sets or replaces the subject's PIN and clears any lockout.
func (s *Service) EnrollPIN(ctx context.Context, c client.Client, subjectID, p string) error {
	if err := validateIDs(subjectID); err != nil {
		return err
	}
	if err := pin.Validate(p, c.PinPolicy); err != nil {
		return ErrPINRejected
	}
	rec, err := pin.Hash(p)
	if err != nil {
		return err
	}
	return s.Factors.PutPIN(ctx, c.ID, subjectID, factor.PIN{Record: rec, FailedCount: 0, LockedUntil: nil, UpdatedAt: s.now()})
}

// DeletePIN removes the subject's PIN; it is idempotent.
func (s *Service) DeletePIN(ctx context.Context, c client.Client, subjectID string) error {
	if err := validateIDs(subjectID); err != nil {
		return err
	}
	return s.Factors.DeletePIN(ctx, c.ID, subjectID)
}

// ListFactors reports the subject's enrolled factors.
func (s *Service) ListFactors(ctx context.Context, c client.Client, subjectID string) (FactorsView, error) {
	if err := validateIDs(subjectID); err != nil {
		return FactorsView{}, err
	}
	view := FactorsView{WebAuthn: []CredentialView{}}
	_, err := s.Factors.GetPIN(ctx, c.ID, subjectID)
	switch {
	case err == nil:
		view.PIN = true
	case !errors.Is(err, factor.ErrNotFound):
		return FactorsView{}, err
	}
	creds, err := s.Factors.ListCredentials(ctx, c.ID, subjectID)
	if err != nil {
		return FactorsView{}, err
	}
	for _, cr := range creds {
		view.WebAuthn = append(view.WebAuthn, CredentialView{
			CredentialID: cr.ID, CreatedAt: cr.CreatedAt, LastUsedAt: cr.LastUsedAt,
		})
	}
	return view, nil
}

// lockedPIN loads the subject's PIN and reports an active lock as Locked.
func (s *Service) lockedPIN(ctx context.Context, clientID, subjectID string, now time.Time) error {
	p, err := s.Factors.GetPIN(ctx, clientID, subjectID)
	if errors.Is(err, factor.ErrNotFound) {
		return ErrFactorNotFound
	}
	if err != nil {
		return err
	}
	if p.LockedUntil != nil && p.LockedUntil.After(now) {
		return Locked{Until: *p.LockedUntil}
	}
	return nil
}

// BeginChallenge opens a single-use challenge for purpose. The assertion
// options are returned only for webauthn; they are nil for pin.
func (s *Service) BeginChallenge(ctx context.Context, c client.Client, subjectID, purpose string, f challenge.Factor) (challenge.Challenge, *protocol.CredentialAssertion, error) {
	if err := validateIDs(subjectID, purpose); err != nil {
		return challenge.Challenge{}, nil, err
	}
	now := s.now()
	ch := challenge.Challenge{
		ID: id.New(), ClientID: c.ID, SubjectID: subjectID, Purpose: purpose, Factor: f,
		Status: challenge.StatusPending, Attempts: 0, ExpiresAt: now.Add(challengeTTL), CreatedAt: now,
	}
	switch f {
	case challenge.FactorPIN:
		if err := s.lockedPIN(ctx, c.ID, subjectID, now); err != nil {
			return challenge.Challenge{}, nil, err
		}
		ch.MaxAttempts = c.PinPolicy.MaxAttempts
	case challenge.FactorWebAuthn:
		creds, err := s.Factors.ListCredentials(ctx, c.ID, subjectID)
		if err != nil {
			return challenge.Challenge{}, nil, err
		}
		if len(creds) == 0 {
			return challenge.Challenge{}, nil, ErrFactorNotFound
		}
		wa, err := webauthn.For(c)
		if err != nil {
			return challenge.Challenge{}, nil, err
		}
		assertion, session, err := wa.BeginLogin(webauthn.NewUser(subjectID, "", creds))
		if err != nil {
			return challenge.Challenge{}, nil, fmt.Errorf("begin login: %w", err)
		}
		if ch.Session, err = json.Marshal(session); err != nil {
			return challenge.Challenge{}, nil, err
		}
		ch.MaxAttempts = webauthnMaxAttempts
		if err := s.Challenges.Create(ctx, ch); err != nil {
			return challenge.Challenge{}, nil, err
		}
		return ch, assertion, nil
	default:
		return challenge.Challenge{}, nil, ErrWrongFactor
	}
	if err := s.Challenges.Create(ctx, ch); err != nil {
		return challenge.Challenge{}, nil, err
	}
	return ch, nil, nil
}

// VerifyPIN spends one attempt on the challenge and, on a correct PIN,
// completes it and issues the assertion token.
func (s *Service) VerifyPIN(ctx context.Context, c client.Client, challengeID, p string) (Token, error) {
	if err := validateIDs(challengeID); err != nil {
		return Token{}, err
	}
	now := s.now()
	ch, err := s.Challenges.Attempt(ctx, c.ID, challengeID, now)
	if err != nil {
		return Token{}, err
	}
	if ch.Factor != challenge.FactorPIN {
		return Token{}, ErrWrongFactor
	}
	rec, err := s.Factors.GetPIN(ctx, c.ID, ch.SubjectID)
	if errors.Is(err, factor.ErrNotFound) {
		return Token{}, ErrFactorNotFound
	}
	if err != nil {
		return Token{}, err
	}
	if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
		return Token{}, Locked{Until: *rec.LockedUntil}
	}
	if !rec.Record.Verify(p) {
		lockout := time.Duration(c.PinPolicy.LockoutSec) * time.Second
		_, until, err := s.Factors.RecordPINFailure(ctx, c.ID, ch.SubjectID, c.PinPolicy.MaxAttempts, lockout, now)
		if errors.Is(err, factor.ErrNotFound) {
			return Token{}, ErrFactorNotFound
		}
		if err != nil {
			return Token{}, err
		}
		if until != nil {
			return Token{}, Locked{Until: *until}
		}
		return Token{}, VerificationFailed{AttemptsRemaining: ch.MaxAttempts - ch.Attempts}
	}
	if err := s.Challenges.Complete(ctx, c.ID, ch.ID, now); err != nil {
		return Token{}, err
	}
	// The challenge is consumed; a failed reset must not cost the caller the token.
	if err := s.Factors.ResetPINFailures(ctx, c.ID, ch.SubjectID); err != nil {
		slog.Error("reset pin failures", "client", c.ID, "err", err)
	}
	return s.issue(ctx, c, ch, now)
}

// VerifyWebAuthn spends one attempt on the challenge and, on a valid
// assertion, completes it, records the credential use and issues the token.
func (s *Service) VerifyWebAuthn(ctx context.Context, c client.Client, challengeID string, body []byte) (Token, error) {
	if err := validateIDs(challengeID); err != nil {
		return Token{}, err
	}
	now := s.now()
	ch, err := s.Challenges.Attempt(ctx, c.ID, challengeID, now)
	if err != nil {
		return Token{}, err
	}
	if ch.Factor != challenge.FactorWebAuthn {
		return Token{}, ErrWrongFactor
	}
	failed := VerificationFailed{AttemptsRemaining: ch.MaxAttempts - ch.Attempts}
	var session gowa.SessionData
	if err := json.Unmarshal(ch.Session, &session); err != nil {
		return Token{}, fmt.Errorf("challenge session: %w", err)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		return Token{}, failed
	}
	creds, err := s.Factors.ListCredentials(ctx, c.ID, ch.SubjectID)
	if err != nil {
		return Token{}, err
	}
	wa, err := webauthn.For(c)
	if err != nil {
		return Token{}, err
	}
	user := webauthn.NewUser(ch.SubjectID, "", creds)
	cred, err := wa.ValidateLogin(user, session, parsed)
	if err != nil || cred.Authenticator.CloneWarning {
		return Token{}, failed
	}
	if err := s.Challenges.Complete(ctx, c.ID, ch.ID, now); err != nil {
		return Token{}, err
	}
	// The challenge is consumed; a failed use update must not cost the caller the token.
	if err := s.Factors.UpdateCredentialUse(ctx, c.ID, ch.SubjectID, webauthn.EncodeID(cred.ID), cred.Authenticator.SignCount, cred.Flags.BackupState, now); err != nil {
		slog.Error("update credential use", "client", c.ID, "err", err)
	}
	return s.issue(ctx, c, ch, now)
}

// BeginRegistration opens a five-minute passkey registration. The subject's
// existing credentials are excluded.
func (s *Service) BeginRegistration(ctx context.Context, c client.Client, subjectID, displayName string) (challenge.Registration, *protocol.CredentialCreation, error) {
	if err := validateIDs(subjectID); err != nil {
		return challenge.Registration{}, nil, err
	}
	if len(subjectID) > webauthn.MaxUserIDLen {
		return challenge.Registration{}, nil, id.ErrInvalid
	}
	creds, err := s.Factors.ListCredentials(ctx, c.ID, subjectID)
	if err != nil {
		return challenge.Registration{}, nil, err
	}
	wa, err := webauthn.For(c)
	if err != nil {
		return challenge.Registration{}, nil, err
	}
	user := webauthn.NewUser(subjectID, displayName, creds)
	exclusions := gowa.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()
	creation, session, err := wa.BeginRegistration(user, gowa.WithExclusions(exclusions))
	if err != nil {
		return challenge.Registration{}, nil, fmt.Errorf("begin registration: %w", err)
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return challenge.Registration{}, nil, err
	}
	reg := challenge.Registration{
		ID: id.New(), ClientID: c.ID, SubjectID: subjectID, DisplayName: displayName,
		Session: raw, ExpiresAt: s.now().Add(challengeTTL),
	}
	if err := s.Challenges.PutRegistration(ctx, reg); err != nil {
		return challenge.Registration{}, nil, err
	}
	return reg, creation, nil
}

// FinishRegistration consumes the registration and stores the verified
// credential, returning its base64url ID.
func (s *Service) FinishRegistration(ctx context.Context, c client.Client, registrationID string, body []byte) (string, error) {
	if err := validateIDs(registrationID); err != nil {
		return "", err
	}
	now := s.now()
	reg, err := s.Challenges.TakeRegistration(ctx, c.ID, registrationID, now)
	if err != nil {
		return "", err
	}
	var session gowa.SessionData
	if err := json.Unmarshal(reg.Session, &session); err != nil {
		return "", fmt.Errorf("registration session: %w", err)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRegistrationFailed, err)
	}
	wa, err := webauthn.For(c)
	if err != nil {
		return "", err
	}
	wc, err := wa.CreateCredential(webauthn.NewUser(reg.SubjectID, reg.DisplayName, nil), session, parsed)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRegistrationFailed, err)
	}
	fc := webauthn.ToCredential(wc, reg.DisplayName, now)
	if len(fc.ID) > webauthn.MaxIDLen {
		return "", fmt.Errorf("%w: credential id too long", ErrRegistrationFailed)
	}
	if err := s.Factors.PutCredential(ctx, c.ID, reg.SubjectID, fc); err != nil {
		return "", err
	}
	return fc.ID, nil
}

// DeleteCredential removes one of the subject's passkeys; it is idempotent.
func (s *Service) DeleteCredential(ctx context.Context, c client.Client, subjectID, credentialID string) error {
	if err := validateIDs(subjectID, credentialID); err != nil {
		return err
	}
	return s.Factors.DeleteCredential(ctx, c.ID, subjectID, credentialID)
}

func (s *Service) issue(ctx context.Context, c client.Client, ch challenge.Challenge, now time.Time) (Token, error) {
	exp := now.Add(time.Duration(c.TokenTTLSec) * time.Second)
	tok, err := token.Issue(ctx, s.Signer, token.Claims{
		Issuer: s.Issuer, Subject: ch.SubjectID, Audience: c.ID, Purpose: ch.Purpose, JTI: ch.ID,
		AMR: []string{string(ch.Factor)}, IssuedAt: now, ExpiresAt: exp,
	})
	if err != nil {
		return Token{}, err
	}
	return Token{Token: tok, ExpiresAt: exp, Factor: ch.Factor}, nil
}

// CreateClient registers a client and returns it with its first raw access
// key. An invalid input wraps client.ErrInvalid.
func (s *Service) CreateClient(ctx context.Context, in client.CreateInput) (client.Client, string, error) {
	return s.Clients.Create(ctx, in)
}

// GetClient returns a client by id; an unknown id is client.ErrNotFound.
func (s *Service) GetClient(ctx context.Context, clientID string) (client.Client, error) {
	if err := validateIDs(clientID); err != nil {
		return client.Client{}, err
	}
	return s.Clients.Get(ctx, clientID)
}

// UpdateClient applies a patch to a client and returns the stored result.
func (s *Service) UpdateClient(ctx context.Context, clientID string, p client.Patch) (client.Client, error) {
	if err := validateIDs(clientID); err != nil {
		return client.Client{}, err
	}
	return s.Clients.Update(ctx, clientID, p)
}

// ListClients returns one page of client summaries and the next cursor.
func (s *Service) ListClients(ctx context.Context, cursor string) ([]client.Summary, string, error) {
	return s.Clients.List(ctx, cursor)
}

// DisableClient stops the client's keys from authenticating.
func (s *Service) DisableClient(ctx context.Context, clientID string) (client.Client, error) {
	if err := validateIDs(clientID); err != nil {
		return client.Client{}, err
	}
	return s.Clients.Disable(ctx, clientID)
}

// EnableClient lets the client's keys authenticate again.
func (s *Service) EnableClient(ctx context.Context, clientID string) (client.Client, error) {
	if err := validateIDs(clientID); err != nil {
		return client.Client{}, err
	}
	return s.Clients.Enable(ctx, clientID)
}

// RevokePreviousKeys ends the grace of every replaced key and returns how many were still valid.
func (s *Service) RevokePreviousKeys(ctx context.Context, clientID string) (int, error) {
	if err := validateIDs(clientID); err != nil {
		return 0, err
	}
	return s.Clients.RevokePreviousKeys(ctx, clientID)
}

// RotateClientKey issues a new access key; previous keys keep working for 24 h.
func (s *Service) RotateClientKey(ctx context.Context, clientID string) (string, error) {
	if err := validateIDs(clientID); err != nil {
		return "", err
	}
	return s.Clients.RotateKey(ctx, clientID)
}

// PurgeSubject deletes the subject's PIN and every passkey of an existing
// client. Pending challenges die by TTL.
func (s *Service) PurgeSubject(ctx context.Context, clientID, subjectID string) error {
	if err := validateIDs(clientID, subjectID); err != nil {
		return err
	}
	if _, err := s.Clients.Get(ctx, clientID); err != nil {
		return err
	}
	return s.Factors.PurgeSubject(ctx, clientID, subjectID)
}
