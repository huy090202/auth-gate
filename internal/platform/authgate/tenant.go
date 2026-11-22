package authgate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// TenantClient calls the tenant-scoped /api/portal/clients routes for one
// workspace. Every call carries Client.PortalKey and the x-tenant-id header, so
// AuthGate itself confines it to that tenant's clients. It never uses the
// admin or client key.
type TenantClient struct {
	c      *Client
	tenant string
}

// Tenant returns the client for the workspace with the given tenant id.
func (c *Client) Tenant(id string) *TenantClient { return &TenantClient{c: c, tenant: id} }

func (t *TenantClient) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	// A missing key or tenant is our misconfiguration: fail before sending, as
	// an AuthGate 401 would be.
	if t.c.PortalKey == "" {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, errors.New("no portal key"))
	}
	if t.tenant == "" {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, errors.New("no tenant id"))
	}
	return t.c.doTenant(ctx, method, path, t.c.PortalKey, t.tenant, body)
}

// List returns one page of the workspace's client summaries; cursor is empty for the first.
func (t *TenantClient) List(ctx context.Context, cursor string) (json.RawMessage, error) {
	path := "/api/portal/clients"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	return t.call(ctx, http.MethodGet, path, nil)
}

// Get returns one client's view.
func (t *TenantClient) Get(ctx context.Context, id string) (json.RawMessage, error) {
	return t.call(ctx, http.MethodGet, "/api/portal/clients/"+seg(id), nil)
}

// Create registers a client in the workspace. The answer carries the new key once.
func (t *TenantClient) Create(ctx context.Context, body any) (json.RawMessage, error) {
	return t.call(ctx, http.MethodPost, "/api/portal/clients", body)
}

// Update patches a client.
func (t *TenantClient) Update(ctx context.Context, id string, body any) (json.RawMessage, error) {
	return t.call(ctx, http.MethodPatch, "/api/portal/clients/"+seg(id), body)
}

// Disable disables a client.
func (t *TenantClient) Disable(ctx context.Context, id string) (json.RawMessage, error) {
	return t.call(ctx, http.MethodPost, "/api/portal/clients/"+seg(id)+"/disable", struct{}{})
}

// Enable enables a client.
func (t *TenantClient) Enable(ctx context.Context, id string) (json.RawMessage, error) {
	return t.call(ctx, http.MethodPost, "/api/portal/clients/"+seg(id)+"/enable", struct{}{})
}

// RotateKey issues a new key. A concurrent rotation answers 409 rotate_conflict.
func (t *TenantClient) RotateKey(ctx context.Context, id string) (json.RawMessage, error) {
	return t.call(ctx, http.MethodPost, "/api/portal/clients/"+seg(id)+"/keys", struct{}{})
}

// RevokePreviousKeys ends the grace period of every previous key at once.
func (t *TenantClient) RevokePreviousKeys(ctx context.Context, id string) (json.RawMessage, error) {
	return t.call(ctx, http.MethodDelete, "/api/portal/clients/"+seg(id)+"/keys/previous", nil)
}

// PurgeSubject deletes every factor a subject has under a client.
func (t *TenantClient) PurgeSubject(ctx context.Context, id, subject string) error {
	_, err := t.call(ctx, http.MethodDelete, "/api/portal/clients/"+seg(id)+"/subjects/"+seg(subject), nil)
	return err
}

// Delete deletes a client: its keys stop at once and its id answers 404 from then on.
func (t *TenantClient) Delete(ctx context.Context, id string) error {
	_, err := t.call(ctx, http.MethodDelete, "/api/portal/clients/"+seg(id), nil)
	return err
}
