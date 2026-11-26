// Package consoletest runs a real AuthGate and a real Console together for the
// Console's tests. Only _test.go files import it, so the Console binary never
// links internal/auth/service; the make check rule on cmd/console still holds.
package consoletest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/handler"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/auth/token"
	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/portalapi"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

const (
	// AdminKey is the key of the AuthGate admin API in the harness.
	AdminKey = "harness-admin-key"
	// RPID and Origin are the Console's relying party, registered at AuthGate.
	RPID   = "console.test"
	Origin = "https://console.test"
)

// Clock is an injectable clock shared by AuthGate and the Console. It starts at
// the real time and only moves forward: a clock in the past would let DynamoDB
// Local's TTL reaper delete items that carry a ttl.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d. A negative d is a test bug.
func (c *Clock) Advance(d time.Duration) {
	if d < 0 {
		panic("consoletest: the clock never moves back")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Passkey is a virtual authenticator with one credential, registered with the
// Console's relying party. It is not yet enrolled at AuthGate.
type Passkey struct {
	RP         virtualwebauthn.RelyingParty
	Authn      virtualwebauthn.Authenticator
	Credential virtualwebauthn.Credential
}

// Harness is a running AuthGate and Console.
type Harness struct {
	T     *testing.T
	Clock *Clock

	// AuthGate side.
	AuthGateURL string
	Clients     *client.Service
	ClientID    string // the Console's client at AuthGate
	ClientKey   string
	Signer      token.Signer

	// Console side.
	Console     http.Handler // call directly with httptest
	ConsoleURL  string       // the same handler on a TLS server
	ConsoleHTTP *http.Client // trusts ConsoleURL
	Deps        console.Deps

	// AuthGate is the Console's own client; Tokens its verifier.
	AuthGate *authgate.Client
	Tokens   *authgate.Verifier

	// Portal is the fake operator API the Console calls.
	Portal *PortalFake

	Passkey Passkey
}

// New starts both servers on two fresh DynamoDB Local tables. The test is
// skipped when DYNAMO_ENDPOINT is unset.
func New(t *testing.T) *Harness {
	t.Helper()
	ctx := context.Background()
	clock := &Clock{t: time.Now()}

	db, agTable := dynamotest.NewTable(t)
	_, conTable := dynamotest.NewTable(t)

	signer, err := token.NewLocalSigner("harness")
	if err != nil {
		t.Fatal(err)
	}
	clients := &client.Service{Store: client.NewDynamoStore(db, agTable), Now: clock.Now}
	svc := &service.Service{
		Clients:    clients,
		Factors:    factor.NewDynamoStore(db, agTable),
		Challenges: challenge.NewDynamoStore(db, agTable),
		Signer:     signer,
		Now:        clock.Now,
	}
	// The three roles share one server under the role prefixes the client adds
	// for a local address, exactly as `make dev` serves them.
	mux := http.NewServeMux()
	mux.Handle("/verify/", http.StripPrefix("/verify", handler.NewVerify(svc, clients, &ratelimit.Limiter{DB: db, Table: agTable, Now: clock.Now})))
	mux.Handle("/admin/", http.StripPrefix("/admin", handler.NewAdmin(svc, AdminKey)))
	mux.Handle("/jwks/", http.StripPrefix("/jwks", handler.NewJWKS(signer)))
	ag := httptest.NewTLSServer(mux)
	t.Cleanup(ag.Close)
	svc.Issuer = ag.URL // the token iss is the base URL the Verifier is given

	c, key, err := clients.Create(ctx, client.CreateInput{Name: "console", RPID: RPID, Origins: []string{Origin}})
	if err != nil {
		t.Fatalf("register the console client: %v", err)
	}

	portal := newPortalFake(t)
	h := &Harness{
		T: t, Clock: clock, Portal: portal,
		AuthGateURL: ag.URL, Clients: clients, ClientID: c.ID, ClientKey: key, Signer: signer,
		AuthGate: &authgate.Client{BaseURL: ag.URL, ClientKey: key, AdminKey: AdminKey, HTTP: ag.Client()},
		Tokens:   &authgate.Verifier{BaseURL: ag.URL, Audience: c.ID, HTTP: ag.Client(), Now: clock.Now},
		Passkey: Passkey{
			RP:         virtualwebauthn.RelyingParty{Name: "Console", ID: RPID, Origin: Origin},
			Authn:      virtualwebauthn.NewAuthenticator(),
			Credential: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
		},
	}
	h.Deps = console.Deps{
		// Not Dev, so tests run what production runs. Requests without a CloudFront
		// viewer address share one rate-limit bucket, which a single host behaves
		// the same under.
		Config: console.Config{
			Table: conTable, AuthGateBaseURL: ag.URL, AuthGateClientID: c.ID,
			PortalURL: portal.Server.URL, PortalClientID: id.New(),
		},
		DB:       db,
		Limiter:  &ratelimit.Limiter{DB: db, Table: conTable, TTL: time.Hour, Now: clock.Now},
		AuthGate: h.AuthGate,
		Tokens:   h.Tokens,
		Portal:   &portalapi.Client{BaseURL: portal.Server.URL, OperatorKey: PortalKey, HTTP: portal.Server.Client()},
		Now:      clock.Now,
	}
	h.Console = console.New(h.Deps)
	cs := httptest.NewTLSServer(h.Console)
	t.Cleanup(cs.Close)
	h.ConsoleURL, h.ConsoleHTTP = cs.URL, cs.Client()
	return h
}
