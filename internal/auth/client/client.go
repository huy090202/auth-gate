// Package client is the registry of calling backends and their access keys.
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

var (
	// ErrNotFound is returned when a client or key item does not exist.
	ErrNotFound = errors.New("client: not found")
	// ErrInvalid is returned for input that fails validation.
	ErrInvalid = errors.New("client: invalid input")
	// ErrUnauthorized is returned for an unknown, expired or malformed access key.
	ErrUnauthorized = errors.New("client: unauthorized")
	// ErrBadCursor is returned for a list cursor that does not decode to a client id.
	ErrBadCursor = errors.New("client: bad cursor")
	// ErrRotateConflict is returned when another rotate changed the client's keys
	// between this rotate's read and its write. Nothing was changed; the caller may retry.
	ErrRotateConflict = errors.New("client: rotate conflict")
	// ErrQuotaExceeded is returned when the owner already has as many clients as its quota allows.
	ErrQuotaExceeded = errors.New("client: quota exceeded")
	// ErrOwnerSuspended is returned when the owner is suspended and may not create clients.
	ErrOwnerSuspended = errors.New("client: owner suspended")
	// ErrDisabledByOperator is returned when a tenant tries to enable or delete a client an operator disabled.
	ErrDisabledByOperator = errors.New("client: disabled by operator")
)

const (
	defaultTokenTTLSec = 300
	maxTokenTTLSec     = 900
	keyGrace           = 24 * time.Hour
	maxRateBurst       = 10000
	maxRatePerSecond   = 5000
	maxClientQuota     = 1000
	defaultOwnerTTL    = 30 * time.Second
	defaultPageSize    = 50
)

// PinPolicy is the per-client PIN rule set.
type PinPolicy struct{ Length, MaxAttempts, LockoutSec int }

// DefaultPinPolicy returns the policy used when a client does not set one.
func DefaultPinPolicy() PinPolicy { return PinPolicy{Length: 6, MaxAttempts: 5, LockoutSec: 900} }

// DefaultRateLimit returns the bucket used when a client does not set one.
func DefaultRateLimit() ratelimit.Settings { return ratelimit.Settings{Burst: 100, PerSecond: 50} }

// DefaultClientQuota is how many clients a tenant may own when no quota is set.
const DefaultClientQuota = 5

// OwnerOf returns the owner value of a tenant's clients; operator clients have "".
func OwnerOf(tenantID string) string { return "T#" + tenantID }

// Owner is a tenant's client counters. SuspendedAt is set while it may not create clients.
type Owner struct {
	TenantID    string
	ClientCount int
	ClientQuota int
	SuspendedAt *time.Time
}

// Client is a registered calling backend. KeyHashes holds the hashes of the
// currently active access keys; it is bookkeeping for rotation and must never
// be returned to callers. RetiredKeys maps the hash of each replaced key to the
// end of its grace; it is bookkeeping for revocation and is never returned either.
type Client struct {
	ID          string
	Owner       string // "" for an operator's client, OwnerOf(tenantId) for a tenant's; never changes
	Name        string
	RPID        string
	Origins     []string
	PinPolicy   PinPolicy
	TokenTTLSec int
	RateLimit   ratelimit.Settings
	CreatedAt   time.Time
	KeyHashes   []string
	RetiredKeys map[string]time.Time
	DisabledAt  *time.Time
	DisabledBy  string     // "operator" or "tenant" when disabled through a route that records it
	DeletedAt   *time.Time // set by a soft delete; the Service treats such a client as not found
}

// DisabledBy says who disabled a client; an operator's disable outranks a tenant's.
type DisabledBy string

const (
	ByOperator DisabledBy = "operator"
	ByTenant   DisabledBy = "tenant"
)

// Summary is the list view of a client.
type Summary struct {
	ID, Name, RPID string
	Owner          string
	CreatedAt      time.Time
	DisabledAt     *time.Time
}

// Patch is a partial update of a client; a nil field is left unchanged. The
// rpId is absent on purpose: existing passkeys are bound to it. A zero field
// of PinPolicy keeps the stored value.
type Patch struct {
	Name        *string
	Origins     []string // nil = unchanged
	PinPolicy   *PinPolicy
	TokenTTLSec *int
	RateLimit   *ratelimit.Settings
}

func (p Patch) empty() bool {
	return p.Name == nil && p.Origins == nil && p.PinPolicy == nil && p.TokenTTLSec == nil && p.RateLimit == nil
}

// Validate checks the patch and returns an error wrapping ErrInvalid.
// perSecond must be above zero: there is no unlimited mode, and a zero rate
// would make the limiter's retry-after infinite.
func (p Patch) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	if p.empty() {
		return bad("nothing to update")
	}
	if r := p.RateLimit; r != nil {
		if r.Burst < 1 || r.Burst > maxRateBurst {
			return bad("rateLimit burst must be 1-%d", maxRateBurst)
		}
		// Written as a negated range so NaN fails too.
		if !(r.PerSecond > 0 && r.PerSecond <= maxRatePerSecond) {
			return bad("rateLimit perSecond must be above 0 and at most %d", maxRatePerSecond)
		}
	}
	return nil
}

// CreateInput is the admin request to register a client.
type CreateInput struct {
	Owner       string // "" for an operator's client, else OwnerOf(tenantId)
	RateLimit   *ratelimit.Settings
	Name        string
	RPID        string
	Origins     []string
	PinPolicy   *PinPolicy
	TokenTTLSec *int
}

// Validate checks the input and returns an error wrapping ErrInvalid.
func (in CreateInput) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	if strings.TrimSpace(in.Name) == "" {
		return bad("name is required")
	}
	if in.Owner != "" && (!strings.HasPrefix(in.Owner, "T#") || id.Validate(strings.TrimPrefix(in.Owner, "T#")) != nil) {
		return bad("owner is not a tenant")
	}
	if in.RateLimit != nil {
		if err := (Patch{RateLimit: in.RateLimit}).Validate(); err != nil {
			return err
		}
	}
	rp := strings.ToLower(in.RPID)
	if rp == "" {
		return bad("rpId is required")
	}
	if !strings.Contains(rp, ".") && rp != "localhost" {
		return bad("rpId %q is not a registrable domain", in.RPID)
	}
	if len(in.Origins) == 0 {
		return bad("at least one origin is required")
	}
	seen := make(map[string]bool, len(in.Origins))
	for _, o := range in.Origins {
		if seen[o] {
			return bad("duplicate origin %q", o)
		}
		seen[o] = true
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return bad("origin %q must be http(s)://host[:port] with no path", o)
		}
		host := strings.ToLower(u.Hostname())
		if host != rp && !strings.HasSuffix(host, "."+rp) {
			return bad("rpId %q is not a suffix of origin host %q", in.RPID, host)
		}
	}
	if p := in.PinPolicy; p != nil {
		if p.Length != 0 && (p.Length < 4 || p.Length > 8) {
			return bad("pin length must be 4-8")
		}
		if p.MaxAttempts < 0 || p.MaxAttempts > 10 {
			return bad("pin maxAttempts must be 0-10")
		}
		if p.LockoutSec < 0 || p.LockoutSec > 86400 {
			return bad("pin lockoutSec must be 0-86400")
		}
	}
	if in.TokenTTLSec != nil && (*in.TokenTTLSec < 1 || *in.TokenTTLSec > maxTokenTTLSec) {
		return bad("tokenTtlSec must be 1-%d", maxTokenTTLSec)
	}
	return nil
}

// Service implements the client use cases on top of a Store.
type Service struct {
	Store    Store
	Now      func() time.Time
	PageSize int // clients per List page; zero means 50
	// OwnerCacheTTL is how long Authenticate trusts a tenant's suspension state
	// on this instance; zero means 30 seconds. A suspension therefore refuses
	// keys within the TTL, not at once.
	OwnerCacheTTL time.Duration

	ownerMu    sync.Mutex
	ownerCache map[string]ownerEntry
}

type ownerEntry struct {
	owner  Owner
	readAt time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create registers a client and returns it with its first raw access key,
// which is shown once.
func (s *Service) Create(ctx context.Context, in CreateInput) (Client, string, error) {
	if err := in.Validate(); err != nil {
		return Client{}, "", err
	}
	raw, hash := NewAccessKey()
	c := Client{
		ID:          id.New(),
		Owner:       in.Owner,
		Name:        in.Name,
		RPID:        in.RPID,
		Origins:     in.Origins,
		PinPolicy:   DefaultPinPolicy(),
		TokenTTLSec: defaultTokenTTLSec,
		RateLimit:   DefaultRateLimit(),
		CreatedAt:   s.now().UTC().Truncate(time.Second),
		KeyHashes:   []string{hash},
	}
	if in.PinPolicy != nil {
		p, d := *in.PinPolicy, DefaultPinPolicy()
		if p.Length == 0 {
			p.Length = d.Length
		}
		if p.MaxAttempts == 0 {
			p.MaxAttempts = d.MaxAttempts
		}
		if p.LockoutSec == 0 {
			p.LockoutSec = d.LockoutSec
		}
		c.PinPolicy = p
	}
	if in.TokenTTLSec != nil {
		c.TokenTTLSec = *in.TokenTTLSec
	}
	if in.RateLimit != nil {
		c.RateLimit = *in.RateLimit
	}
	// Key first: a client without a usable key is harmless, a key without a client is not reachable.
	if err := s.Store.PutKey(ctx, hash, c.ID, nil); err != nil {
		return Client{}, "", err
	}
	if err := s.Store.CreateClient(ctx, c); err != nil {
		if errors.Is(err, ErrQuotaExceeded) || errors.Is(err, ErrOwnerSuspended) {
			// Definitely refused: the key item reaches nothing, so do not leave it.
			if derr := s.Store.DeleteKey(ctx, hash); derr != nil {
				slog.Warn("client: delete key of a refused create", "error", derr)
			}
		}
		return Client{}, "", err
	}
	return c, raw, nil
}

// Get returns a client by id; a deleted client is ErrNotFound.
func (s *Service) Get(ctx context.Context, clientID string) (Client, error) {
	return s.live(ctx, clientID)
}

// live reads a client and hides a soft-deleted one.
func (s *Service) live(ctx context.Context, clientID string) (Client, error) {
	c, err := s.Store.GetClient(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	if c.DeletedAt != nil {
		return Client{}, ErrNotFound
	}
	return c, nil
}

// Delete soft-deletes a client: it stops authenticating at once, leaves every
// list and frees the owner's quota slot. Its key items are then removed best
// effort; Authenticate already refuses a deleted client, so a failure there is
// only logged. Deleting a missing or already deleted client is ErrNotFound.
func (s *Service) Delete(ctx context.Context, clientID string) error {
	return s.DeleteBy(ctx, clientID, ByOperator)
}

// DeleteBy is Delete for a caller of the given kind. A tenant cannot delete a
// client an operator disabled (ErrDisabledByOperator).
func (s *Service) DeleteBy(ctx context.Context, clientID string, by DisabledBy) error {
	c, err := s.live(ctx, clientID)
	if err != nil {
		return err
	}
	if by != ByOperator && c.DisabledAt != nil && c.DisabledBy == string(ByOperator) {
		return ErrDisabledByOperator
	}
	if err := s.Store.DeleteClient(ctx, c, s.now().UTC().Truncate(time.Second), by); err != nil {
		return err
	}
	hashes := append([]string(nil), c.KeyHashes...)
	for h := range c.RetiredKeys {
		hashes = append(hashes, h)
	}
	for _, h := range hashes {
		if err := s.Store.DeleteKey(ctx, h); err != nil {
			slog.Warn("client: delete key of a deleted client", "client", clientID, "error", err)
		}
	}
	return nil
}

// ListOwned returns one page of a tenant's client summaries, newest first, and
// the cursor of the next page (empty on the last).
func (s *Service) ListOwned(ctx context.Context, tenantID, cursor string) ([]Summary, string, error) {
	if err := id.Validate(tenantID); err != nil {
		return nil, "", fmt.Errorf("%w: tenant id", ErrInvalid)
	}
	return s.Store.ListOwned(ctx, tenantID, cursor, s.pageSize())
}

// GetOwner returns a tenant's counters; an unknown tenant has the defaults.
func (s *Service) GetOwner(ctx context.Context, tenantID string) (Owner, error) {
	if err := id.Validate(tenantID); err != nil {
		return Owner{}, fmt.Errorf("%w: tenant id", ErrInvalid)
	}
	return s.Store.GetOwner(ctx, tenantID)
}

// SetQuota sets how many clients a tenant may own, 0 to 1000. Lowering it below
// the current count keeps the existing clients and refuses further creates; 0
// freezes creation.
func (s *Service) SetQuota(ctx context.Context, tenantID string, quota int) (Owner, error) {
	if err := id.Validate(tenantID); err != nil {
		return Owner{}, fmt.Errorf("%w: tenant id", ErrInvalid)
	}
	if quota < 0 || quota > maxClientQuota {
		return Owner{}, fmt.Errorf("%w: quota must be 0-%d", ErrInvalid, maxClientQuota)
	}
	if err := s.Store.SetQuota(ctx, tenantID, quota); err != nil {
		return Owner{}, err
	}
	return s.Store.GetOwner(ctx, tenantID)
}

// RotateKey issues a new active key; every previously active key keeps working
// for 24 hours and then stops authenticating. Replaced keys are recorded in
// retiredKeys so RevokePreviousKeys can find them; entries past their grace
// are dropped here. A disabled client stays disabled.
func (s *Service) RotateKey(ctx context.Context, clientID string) (string, error) {
	c, err := s.live(ctx, clientID)
	if err != nil {
		return "", err
	}
	raw, hash := NewAccessKey()
	if err := s.Store.PutKey(ctx, hash, c.ID, nil); err != nil {
		return "", err
	}
	now := s.now()
	expireAt := now.Add(keyGrace)
	retired := make(map[string]time.Time, len(c.RetiredKeys)+len(c.KeyHashes))
	for h, exp := range c.RetiredKeys {
		if exp.After(now) {
			retired[h] = exp
		}
	}
	for _, old := range c.KeyHashes {
		if err := s.Store.ExpireKey(ctx, old, expireAt); err != nil {
			return "", err
		}
		retired[old] = expireAt.Truncate(time.Second)
	}
	if err := s.Store.SetKeys(ctx, c.ID, c.KeyHashes, []string{hash}, retired); err != nil {
		if errors.Is(err, ErrRotateConflict) || errors.Is(err, ErrNotFound) {
			// The new key item must not outlive the failed rotate: nothing records it.
			if derr := s.Store.DeleteKey(ctx, hash); derr != nil {
				return "", derr
			}
		}
		return "", err
	}
	return raw, nil
}

// RevokePreviousKeys ends the grace of every replaced key at once and returns
// how many were still inside it. The current key is untouched. A retry after a
// partial failure is safe: deleting a missing key item is not an error.
func (s *Service) RevokePreviousKeys(ctx context.Context, clientID string) (int, error) {
	c, err := s.live(ctx, clientID)
	if err != nil {
		return 0, err
	}
	now := s.now()
	revoked := 0
	hashes := make([]string, 0, len(c.RetiredKeys))
	for h, exp := range c.RetiredKeys {
		if err := s.Store.DeleteKey(ctx, h); err != nil {
			return 0, err
		}
		hashes = append(hashes, h)
		if exp.After(now) {
			revoked++
		}
	}
	// Remove only the entries read above: a rotate that ran meanwhile added others.
	if len(hashes) > 0 {
		if err := s.Store.ClearRetiredKeys(ctx, c.ID, hashes); err != nil {
			return 0, err
		}
	}
	return revoked, nil
}

// List returns one page of client summaries, newest first, and the cursor of
// the next page (empty on the last). A bad cursor wraps ErrBadCursor.
func (s *Service) List(ctx context.Context, cursor string) ([]Summary, string, error) {
	return s.Store.ListClients(ctx, cursor, s.pageSize())
}

func (s *Service) pageSize() int32 {
	if s.PageSize <= 0 || s.PageSize > math.MaxInt32 {
		return defaultPageSize
	}
	return int32(s.PageSize)
}

// Disable stops the client's keys from authenticating and records who did it.
// Disabling again keeps the first time, and a tenant never replaces an
// operator's mark.
func (s *Service) Disable(ctx context.Context, clientID string, by DisabledBy) (Client, error) {
	c, err := s.live(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	at := c.DisabledAt
	if at == nil {
		t := s.now().UTC().Truncate(time.Second)
		at = &t
	}
	if err := s.Store.SetDisabled(ctx, c, at, by); err != nil {
		return Client{}, err
	}
	return s.live(ctx, clientID)
}

// Enable clears the disabled mark. A tenant cannot clear one an operator set
// (ErrDisabledByOperator); an operator can clear any.
func (s *Service) Enable(ctx context.Context, clientID string, by DisabledBy) (Client, error) {
	c, err := s.live(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	if by != ByOperator && c.DisabledAt != nil && c.DisabledBy == string(ByOperator) {
		return Client{}, ErrDisabledByOperator
	}
	if err := s.Store.SetDisabled(ctx, c, nil, by); err != nil {
		return Client{}, err
	}
	return s.live(ctx, clientID)
}

// Suspend stops a tenant from creating clients and, once this instance's owner
// cache expires, stops its clients' keys from authenticating. It creates the
// tenant's counters item if there is none; the quota defaults still apply.
func (s *Service) Suspend(ctx context.Context, tenantID string) (Owner, error) {
	at := s.now().UTC().Truncate(time.Second)
	return s.setSuspended(ctx, tenantID, &at)
}

// Unsuspend lifts a suspension.
func (s *Service) Unsuspend(ctx context.Context, tenantID string) (Owner, error) {
	return s.setSuspended(ctx, tenantID, nil)
}

func (s *Service) setSuspended(ctx context.Context, tenantID string, at *time.Time) (Owner, error) {
	if err := id.Validate(tenantID); err != nil {
		return Owner{}, fmt.Errorf("%w: tenant id", ErrInvalid)
	}
	if err := s.Store.SetSuspended(ctx, tenantID, at); err != nil {
		return Owner{}, err
	}
	s.ownerMu.Lock()
	delete(s.ownerCache, tenantID)
	s.ownerMu.Unlock()
	return s.Store.GetOwner(ctx, tenantID)
}

// cachedOwner returns the tenant's counters from this instance's cache while
// the entry is younger than the TTL, else reads and caches them.
func (s *Service) cachedOwner(ctx context.Context, tenantID string) (Owner, error) {
	ttl := s.OwnerCacheTTL
	if ttl <= 0 {
		ttl = defaultOwnerTTL
	}
	now := s.now()
	s.ownerMu.Lock()
	e, ok := s.ownerCache[tenantID]
	s.ownerMu.Unlock()
	if ok && now.Sub(e.readAt) < ttl {
		return e.owner, nil
	}
	o, err := s.Store.GetOwner(ctx, tenantID)
	if err != nil {
		return Owner{}, err
	}
	s.ownerMu.Lock()
	if s.ownerCache == nil {
		s.ownerCache = map[string]ownerEntry{}
	}
	s.ownerCache[tenantID] = ownerEntry{owner: o, readAt: now}
	s.ownerMu.Unlock()
	return o, nil
}

// Update applies a patch to an existing client and returns the stored result.
// The patch is merged into the stored client and validated as a whole, so
// origins still pass the rpId suffix check. An invalid patch wraps ErrInvalid;
// an unknown client is ErrNotFound.
func (s *Service) Update(ctx context.Context, id string, p Patch) (Client, error) {
	c, err := s.live(ctx, id)
	if err != nil {
		return Client{}, err
	}
	if err := p.Validate(); err != nil {
		return Client{}, err
	}
	merged := CreateInput{Name: c.Name, RPID: c.RPID, Origins: c.Origins, PinPolicy: &c.PinPolicy, TokenTTLSec: &c.TokenTTLSec}
	if p.Name != nil {
		merged.Name = *p.Name
	}
	if p.Origins != nil {
		merged.Origins = p.Origins
	}
	if p.TokenTTLSec != nil {
		merged.TokenTTLSec = p.TokenTTLSec
	}
	if pp := p.PinPolicy; pp != nil {
		m := c.PinPolicy
		if pp.Length != 0 {
			m.Length = pp.Length
		}
		if pp.MaxAttempts != 0 {
			m.MaxAttempts = pp.MaxAttempts
		}
		if pp.LockoutSec != 0 {
			m.LockoutSec = pp.LockoutSec
		}
		merged.PinPolicy = &m
		p.PinPolicy = &m
	}
	if err := merged.Validate(); err != nil {
		return Client{}, err
	}
	if err := s.Store.UpdateClient(ctx, c, p); err != nil {
		return Client{}, err
	}
	return s.live(ctx, id)
}

// Authenticate resolves a raw access key to its client. It returns
// ErrUnauthorized for unknown keys and for keys past their expiry, even while
// the key item still exists.
func (s *Service) Authenticate(ctx context.Context, rawKey string) (Client, error) {
	if rawKey == "" {
		return Client{}, ErrUnauthorized
	}
	clientID, expiresAt, err := s.Store.GetKey(ctx, HashKey(rawKey))
	if errors.Is(err, ErrNotFound) {
		return Client{}, ErrUnauthorized
	}
	if err != nil {
		return Client{}, err
	}
	if expiresAt != nil && expiresAt.Before(s.now()) {
		return Client{}, ErrUnauthorized
	}
	c, err := s.live(ctx, clientID)
	if errors.Is(err, ErrNotFound) {
		return Client{}, ErrUnauthorized
	}
	if err != nil {
		return Client{}, err
	}
	if c.DisabledAt != nil {
		return Client{}, ErrUnauthorized
	}
	if c.Owner != "" {
		// Operator clients never read the owner item. A failed read denies (fail closed).
		o, err := s.cachedOwner(ctx, tenantOf(c.Owner))
		if err != nil {
			slog.Warn("client: owner read failed, denying the key", "client", c.ID, "error", err)
			return Client{}, ErrUnauthorized
		}
		if o.SuspendedAt != nil {
			return Client{}, ErrUnauthorized
		}
	}
	return c, nil
}

// KeyFunc adapts Authenticate for httpx.RequireKey; the client is put in ctx.
// A store failure denies the request (fail closed).
func (s *Service) KeyFunc() httpx.KeyFunc {
	return func(ctx context.Context, key string) (context.Context, bool) {
		c, err := s.Authenticate(ctx, key)
		if err != nil {
			return ctx, false
		}
		return WithClient(ctx, c), true
	}
}
