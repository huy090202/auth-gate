// Command smoke runs the real WebAuthn round trip against a running auth-gate:
// create a client, register a virtual passkey, open a challenge, answer it, and
// verify the token against the published JWKS. Bruno has no authenticator, so
// this is the part of the integration check the collection cannot do.
//
//	ADMIN_KEY=... go run ./cmd/smoke -base-url https://auth.example.com
//
// It prints one line per step and exits 0 only if the token's claims match.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/huy090202/auth-gate/internal/auth/token"
)

const (
	rpID    = "smoke.local"
	origin  = "https://smoke.local"
	purpose = "smoke"
)

func main() {
	baseURL := flag.String("base-url", "", "auth-gate base URL (the CloudFront or custom domain; token iss)")
	adminKey := flag.String("admin-key", os.Getenv("ADMIN_KEY"), "admin key (default $ADMIN_KEY, which keeps it out of the process list)")
	flag.Parse()
	if *baseURL == "" || *adminKey == "" {
		fmt.Fprintln(os.Stderr, "usage: smoke -base-url URL -admin-key KEY")
		os.Exit(2)
	}
	if err := run(strings.TrimRight(*baseURL, "/"), *adminKey); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
}

func run(base, adminKey string) error {
	api, err := newAPI(base)
	if err != nil {
		return err
	}

	var created struct{ ClientID, AccessKey string }
	if err := api.call("POST", "/api/clients", adminKey, map[string]any{
		"name": "smoke", "rpId": rpID, "origins": []string{origin},
	}, http.StatusCreated, &created); err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	if created.AccessKey == adminKey {
		return errors.New("create client: client key equals admin key")
	}
	key := created.AccessKey
	fmt.Println("ok client created", created.ClientID)

	subject := "smoke-" + randHex(6)
	rp := virtualwebauthn.RelyingParty{Name: "smoke", ID: rpID, Origin: origin}
	auth := virtualwebauthn.NewAuthenticator()
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

	var reg struct {
		RegistrationID string          `json:"registrationId"`
		PublicKey      json.RawMessage `json:"publicKey"`
	}
	if err := api.call("POST", "/api/webauthn/registrations", key, map[string]any{
		"subjectId": subject, "displayName": "Smoke",
	}, http.StatusCreated, &reg); err != nil {
		return fmt.Errorf("begin registration: %w", err)
	}
	ao, err := virtualwebauthn.ParseAttestationOptions(string(reg.PublicKey))
	if err != nil {
		return fmt.Errorf("parse creation options: %w", err)
	}
	if ao.RelyingPartyID != rpID {
		return fmt.Errorf("begin registration: rp.id = %q, want %q", ao.RelyingPartyID, rpID)
	}
	fmt.Println("ok registration options issued")

	attestation := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *ao)
	var finished struct{ CredentialID string }
	if err := api.call("POST", "/api/webauthn/registrations/"+reg.RegistrationID, key, map[string]any{
		"credential": json.RawMessage(attestation),
	}, http.StatusCreated, &finished); err != nil {
		return fmt.Errorf("finish registration: %w", err)
	}
	auth.AddCredential(cred)
	fmt.Println("ok passkey registered", finished.CredentialID)

	var ch struct {
		ChallengeID string          `json:"challengeId"`
		PublicKey   json.RawMessage `json:"publicKey"`
	}
	if err := api.call("POST", "/api/challenges", key, map[string]any{
		"subjectId": subject, "purpose": purpose, "factor": "webauthn",
	}, http.StatusCreated, &ch); err != nil {
		return fmt.Errorf("begin challenge: %w", err)
	}
	asO, err := virtualwebauthn.ParseAssertionOptions(string(ch.PublicKey))
	if err != nil {
		return fmt.Errorf("parse request options: %w", err)
	}
	fmt.Println("ok challenge opened", ch.ChallengeID)

	// virtualwebauthn does not bump the sign counter; a real authenticator does.
	cred.Counter++
	assertion := virtualwebauthn.CreateAssertionResponse(rp, auth, cred, *asO)
	var verified struct{ Token, Factor string }
	if err := api.call("POST", "/api/challenges/"+ch.ChallengeID+"/verify", key, map[string]any{
		"credential": json.RawMessage(assertion),
	}, http.StatusOK, &verified); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if verified.Factor != "webauthn" {
		return fmt.Errorf("verify: factor = %q", verified.Factor)
	}
	fmt.Println("ok assertion verified")

	var jwks map[string]any
	if err := api.call("GET", "/.well-known/jwks.json", "", nil, http.StatusOK, &jwks); err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	fmt.Println("ok jwks fetched")

	claims, err := token.VerifyWithJWKS(verified.Token, jwks)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	switch {
	case claims.Issuer != base:
		return fmt.Errorf("token: iss = %q, want %q", claims.Issuer, base)
	case claims.Audience != created.ClientID:
		return fmt.Errorf("token: aud = %q, want %q", claims.Audience, created.ClientID)
	case claims.Subject != subject:
		return fmt.Errorf("token: sub = %q, want %q", claims.Subject, subject)
	case claims.Purpose != purpose:
		return fmt.Errorf("token: purpose = %q, want %q", claims.Purpose, purpose)
	case claims.JTI != ch.ChallengeID:
		return fmt.Errorf("token: jti = %q, want %q", claims.JTI, ch.ChallengeID)
	case !slices.Equal(claims.AMR, []string{"webauthn"}):
		return fmt.Errorf("token: amr = %v", claims.AMR)
	}
	fmt.Println("ok token verified")

	// Disabling a client cuts its key off at once but does not unsign what it was
	// already issued: the token still verifies against the JWKS. Enabling restores
	// the same key.
	if err := api.call("POST", "/api/clients/"+created.ClientID+"/disable", adminKey, nil, http.StatusOK, nil); err != nil {
		return fmt.Errorf("disable: %w", err)
	}
	if err := api.call("GET", "/api/factors/"+subject, key, nil, http.StatusUnauthorized, nil); err != nil {
		return fmt.Errorf("client call while disabled: %w", err)
	}
	fmt.Println("ok disabled client refused")
	if _, err := token.VerifyWithJWKS(verified.Token, jwks); err != nil {
		return fmt.Errorf("token while client disabled: %w", err)
	}
	fmt.Println("ok token still verifies")
	if err := api.call("POST", "/api/clients/"+created.ClientID+"/enable", adminKey, nil, http.StatusOK, nil); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	if err := api.call("GET", "/api/factors/"+subject, key, nil, http.StatusOK, nil); err != nil {
		return fmt.Errorf("client call after enable: %w", err)
	}
	fmt.Println("ok enabled client accepted")
	return nil
}

// api calls auth-gate at base. Against a localhost base (make dev), it adds
// the role prefix CloudFront would otherwise route by, like the Bruno
// collection's pre-request script.
type api struct {
	base  string
	local bool
	http  *http.Client
}

func newAPI(base string) (*api, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("bad -base-url %q", base)
	}
	h := u.Hostname()
	return &api{base: base, local: h == "localhost" || h == "127.0.0.1", http: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (a *api) url(path string) string {
	if !a.local {
		return a.base + path
	}
	switch {
	case strings.HasPrefix(path, "/api/clients"):
		return a.base + "/admin" + path
	case strings.HasPrefix(path, "/.well-known/"):
		return a.base + "/jwks" + path
	default:
		return a.base + "/verify" + path
	}
}

func (a *api) call(method, path, key string, body any, want int, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.url(path), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("x-internal-access-key", key)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		// The error body never carries key material; print it for diagnosis.
		return fmt.Errorf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, bytes.TrimSpace(raw))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
