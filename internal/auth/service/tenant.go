package service

import (
	"context"
	"fmt"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// tenantRateLimit is the bucket of every client a tenant creates, whatever it asks for.
var tenantRateLimit = ratelimit.Settings{Burst: 20, PerSecond: 10}

// Tenant is the service as one tenant sees it through the portal. A client
// whose owner is another tenant, an operator ("") or that is deleted answers
// client.ErrNotFound, exactly like an unknown id. While the tenant is
// suspended every write answers client.ErrOwnerSuspended.
type Tenant struct {
	s     *Service
	id    string
	owner string
}

// Tenant scopes the service to one tenant. A tenant id that is not a
// canonical ULID is id.ErrInvalid.
func (s *Service) Tenant(tenantID string) (*Tenant, error) {
	if err := id.ValidateULID(tenantID); err != nil {
		return nil, err
	}
	return &Tenant{s: s, id: tenantID, owner: client.OwnerOf(tenantID)}, nil
}

// own loads a live client of this tenant. The owner never changes after
// creation, so the check holds for the write that follows; a delete in
// between is refused by the store's conditions.
func (t *Tenant) own(ctx context.Context, clientID string) (client.Client, error) {
	if err := validateIDs(clientID); err != nil {
		return client.Client{}, err
	}
	c, err := t.s.Clients.Get(ctx, clientID)
	if err != nil {
		return client.Client{}, err
	}
	if c.Owner != t.owner {
		return client.Client{}, client.ErrNotFound
	}
	return c, nil
}

// writable loads a client of this tenant for a write, refusing while the
// tenant is suspended. The owner is read from the store, not the key cache.
func (t *Tenant) writable(ctx context.Context, clientID string) (client.Client, error) {
	c, err := t.own(ctx, clientID)
	if err != nil {
		return client.Client{}, err
	}
	o, err := t.s.Clients.GetOwner(ctx, t.id)
	if err != nil {
		return client.Client{}, err
	}
	if o.SuspendedAt != nil {
		return client.Client{}, client.ErrOwnerSuspended
	}
	return c, nil
}

// Create registers a client owned by this tenant with the tenant rate limit.
// Quota and suspension are checked in the store's transaction.
func (t *Tenant) Create(ctx context.Context, in client.CreateInput) (client.Client, string, error) {
	in.Owner = t.owner
	rl := tenantRateLimit
	in.RateLimit = &rl
	return t.s.Clients.Create(ctx, in)
}

// List returns one page of this tenant's client summaries and the next cursor.
func (t *Tenant) List(ctx context.Context, cursor string) ([]client.Summary, string, error) {
	return t.s.Clients.ListOwned(ctx, t.id, cursor)
}

// Get returns one of this tenant's clients.
func (t *Tenant) Get(ctx context.Context, clientID string) (client.Client, error) {
	return t.own(ctx, clientID)
}

// Update applies a patch to one of this tenant's clients. The rate limit is
// the operator's to set: a patch carrying one wraps client.ErrInvalid.
func (t *Tenant) Update(ctx context.Context, clientID string, p client.Patch) (client.Client, error) {
	if _, err := t.writable(ctx, clientID); err != nil {
		return client.Client{}, err
	}
	if p.RateLimit != nil {
		return client.Client{}, fmt.Errorf("%w: rateLimit is set by the operator", client.ErrInvalid)
	}
	return t.s.Clients.Update(ctx, clientID, p)
}

// Disable stops the client's keys, marked as the tenant's doing.
func (t *Tenant) Disable(ctx context.Context, clientID string) (client.Client, error) {
	if _, err := t.writable(ctx, clientID); err != nil {
		return client.Client{}, err
	}
	return t.s.Clients.Disable(ctx, clientID, client.ByTenant)
}

// Enable lets the client's keys work again, unless an operator disabled it
// (client.ErrDisabledByOperator).
func (t *Tenant) Enable(ctx context.Context, clientID string) (client.Client, error) {
	if _, err := t.writable(ctx, clientID); err != nil {
		return client.Client{}, err
	}
	return t.s.Clients.Enable(ctx, clientID, client.ByTenant)
}

// RotateKey issues a new access key; previous keys keep working for 24 h.
func (t *Tenant) RotateKey(ctx context.Context, clientID string) (string, error) {
	if _, err := t.writable(ctx, clientID); err != nil {
		return "", err
	}
	return t.s.rotate(ctx, clientID)
}

// RevokePreviousKeys ends the grace of every replaced key and returns how many were still valid.
func (t *Tenant) RevokePreviousKeys(ctx context.Context, clientID string) (int, error) {
	if _, err := t.writable(ctx, clientID); err != nil {
		return 0, err
	}
	return t.s.Clients.RevokePreviousKeys(ctx, clientID)
}

// PurgeSubject deletes one end user's PIN and passkeys on this tenant's client.
func (t *Tenant) PurgeSubject(ctx context.Context, clientID, subjectID string) error {
	if err := validateIDs(subjectID); err != nil {
		return err
	}
	if _, err := t.writable(ctx, clientID); err != nil {
		return err
	}
	return t.s.Factors.PurgeSubject(ctx, clientID, subjectID)
}

// Delete soft-deletes one of this tenant's clients and frees its quota slot,
// unless an operator disabled it (client.ErrDisabledByOperator).
func (t *Tenant) Delete(ctx context.Context, clientID string) error {
	if _, err := t.writable(ctx, clientID); err != nil {
		return err
	}
	return t.s.Clients.DeleteBy(ctx, clientID, client.ByTenant)
}
