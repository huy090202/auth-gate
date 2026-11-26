package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// NewAdmin returns the admin-role API behind the static admin key.
// The origin-verify wrapper is applied in main.
func NewAdmin(svc *service.Service, adminKey string) http.Handler {
	h := &admin{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/clients", h.create)
	mux.HandleFunc("GET /api/clients", h.list)
	mux.HandleFunc("GET /api/clients/{id}", h.get)
	mux.HandleFunc("PATCH /api/clients/{id}", h.patch)
	mux.HandleFunc("POST /api/clients/{id}/disable", h.disable)
	mux.HandleFunc("POST /api/clients/{id}/enable", h.enable)
	mux.HandleFunc("POST /api/clients/{id}/keys", h.rotate)
	mux.HandleFunc("DELETE /api/clients/{id}/keys/previous", h.revokePrevious)
	mux.HandleFunc("DELETE /api/clients/{id}/subjects/{subjectId}", h.purge)
	mux.HandleFunc("DELETE /api/clients/{id}", h.delete)
	mux.HandleFunc("GET /api/owners/{tenantId}", h.getOwner)
	mux.HandleFunc("PATCH /api/owners/{tenantId}", h.patchOwner)
	mux.HandleFunc("POST /api/owners/{tenantId}/suspend", h.suspendOwner)
	mux.HandleFunc("POST /api/owners/{tenantId}/unsuspend", h.unsuspendOwner)
	return httpx.RequireKey("x-internal-access-key", httpx.StaticKey(adminKey))(mux)
}

type admin struct{ svc *service.Service }

type pinPolicyView struct {
	Length      int `json:"length"`
	MaxAttempts int `json:"maxAttempts"`
	LockoutSec  int `json:"lockoutSec"`
}

// clientView is the public view of a client. It deliberately has no key material.
type clientView struct {
	ClientID    string        `json:"clientId"`
	Name        string        `json:"name"`
	RPID        string        `json:"rpId"`
	Origins     []string      `json:"origins"`
	PinPolicy   pinPolicyView `json:"pinPolicy"`
	TokenTTLSec int           `json:"tokenTtlSec"`
	RateLimit   rateLimitView `json:"rateLimit"`
	CreatedAt   string        `json:"createdAt"`
	DisabledAt  string        `json:"disabledAt,omitempty"`
	DisabledBy  string        `json:"disabledBy,omitempty"`
	Owner       string        `json:"owner,omitempty"` // the tenant id; absent on an operator's client
}

// summaryView is the list view of a client.
type summaryView struct {
	ClientID   string `json:"clientId"`
	Name       string `json:"name"`
	RPID       string `json:"rpId"`
	CreatedAt  string `json:"createdAt"`
	DisabledAt string `json:"disabledAt,omitempty"`
	Owner      string `json:"owner,omitempty"`
}

func summaryOf(s client.Summary) summaryView {
	return summaryView{
		ClientID: s.ID, Name: s.Name, RPID: s.RPID, Owner: tenantOf(s.Owner),
		CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339), DisabledAt: rfc3339(s.DisabledAt),
	}
}

// tenantOf returns the tenant id of an owner value, "" for an operator's client.
func tenantOf(owner string) string {
	t, ok := strings.CutPrefix(owner, "T#")
	if !ok {
		return ""
	}
	return t
}

func rfc3339(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type rateLimitView struct {
	Burst     int     `json:"burst"`
	PerSecond float64 `json:"perSecond"`
}

// decodeCreate reads a create body. It has no owner and no rateLimit: a
// body field of either name is ignored.
func decodeCreate(r *http.Request) (client.CreateInput, error) {
	var body struct {
		Name        string         `json:"name"`
		RPID        string         `json:"rpId"`
		Origins     []string       `json:"origins"`
		PinPolicy   *pinPolicyView `json:"pinPolicy"`
		TokenTTLSec *int           `json:"tokenTtlSec"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return client.CreateInput{}, err
	}
	in := client.CreateInput{Name: body.Name, RPID: body.RPID, Origins: body.Origins, TokenTTLSec: body.TokenTTLSec}
	if p := body.PinPolicy; p != nil {
		in.PinPolicy = &client.PinPolicy{Length: p.Length, MaxAttempts: p.MaxAttempts, LockoutSec: p.LockoutSec}
	}
	return in, nil
}

func writeCreated(w http.ResponseWriter, c client.Client, key string) {
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"clientId": c.ID, "accessKey": key})
}

func (h *admin) create(w http.ResponseWriter, r *http.Request) {
	in, err := decodeCreate(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	c, key, err := h.svc.CreateClient(r.Context(), in)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeCreated(w, c, key)
}

func (h *admin) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.GetClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

// decodePatch reads a patch body. The rpId is never patchable: any presence,
// even null, wraps client.ErrInvalid.
func decodePatch(r *http.Request) (client.Patch, error) {
	var body struct {
		Name        *string         `json:"name"`
		Origins     []string        `json:"origins"`
		PinPolicy   *pinPolicyView  `json:"pinPolicy"`
		TokenTTLSec *int            `json:"tokenTtlSec"`
		RateLimit   *rateLimitView  `json:"rateLimit"`
		RPID        json.RawMessage `json:"rpId"` // never patchable; any presence, even null, is rejected
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return client.Patch{}, err
	}
	if body.RPID != nil {
		return client.Patch{}, fmt.Errorf("%w: rpId cannot change", client.ErrInvalid)
	}
	p := client.Patch{Name: body.Name, Origins: body.Origins, TokenTTLSec: body.TokenTTLSec}
	if pp := body.PinPolicy; pp != nil {
		p.PinPolicy = &client.PinPolicy{Length: pp.Length, MaxAttempts: pp.MaxAttempts, LockoutSec: pp.LockoutSec}
	}
	if rl := body.RateLimit; rl != nil {
		p.RateLimit = &ratelimit.Settings{Burst: rl.Burst, PerSecond: rl.PerSecond}
	}
	return p, nil
}

func (h *admin) patch(w http.ResponseWriter, r *http.Request) {
	p, err := decodePatch(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	c, err := h.svc.UpdateClient(r.Context(), r.PathValue("id"), p)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

func (h *admin) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		items []client.Summary
		next  string
		err   error
	)
	if q.Has("owner") {
		items, next, err = h.svc.ListOwnerClients(r.Context(), q.Get("owner"), q.Get("cursor"))
		if errors.Is(err, id.ErrInvalid) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_owner", nil)
			return
		}
	} else {
		items, next, err = h.svc.ListClients(r.Context(), q.Get("cursor"))
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeList(w, items, next)
}

func writeList(w http.ResponseWriter, items []client.Summary, next string) {
	views := make([]summaryView, 0, len(items))
	for _, s := range items {
		views = append(views, summaryOf(s))
	}
	resp := map[string]any{"clients": views}
	if next != "" {
		resp["nextCursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// disable and enable ignore the request body, so an empty one works.
func (h *admin) disable(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.DisableClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

func (h *admin) enable(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.EnableClient(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

func (h *admin) revokePrevious(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.RevokePreviousKeys(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeRevoked(w, n)
}

func writeRevoked(w http.ResponseWriter, n int) {
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"revoked": n})
}

func viewOf(c client.Client) clientView {
	origins := c.Origins
	if origins == nil {
		origins = []string{}
	}
	return clientView{
		ClientID: c.ID, Name: c.Name, RPID: c.RPID, Origins: origins,
		PinPolicy:   pinPolicyView{Length: c.PinPolicy.Length, MaxAttempts: c.PinPolicy.MaxAttempts, LockoutSec: c.PinPolicy.LockoutSec},
		TokenTTLSec: c.TokenTTLSec,
		RateLimit:   rateLimitView{Burst: c.RateLimit.Burst, PerSecond: c.RateLimit.PerSecond},
		CreatedAt:   c.CreatedAt.UTC().Format(time.RFC3339),
		DisabledAt:  rfc3339(c.DisabledAt),
		DisabledBy:  disabledBy(c),
		Owner:       tenantOf(c.Owner),
	}
}

// disabledBy is shown only on a disabled client.
func disabledBy(c client.Client) string {
	if c.DisabledAt == nil {
		return ""
	}
	return c.DisabledBy
}

func (h *admin) rotate(w http.ResponseWriter, r *http.Request) {
	key, err := h.svc.RotateClientKey(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeKey(w, key)
}

func writeKey(w http.ResponseWriter, key string) {
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"accessKey": key})
}

func (h *admin) purge(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.PurgeSubject(r.Context(), r.PathValue("id"), r.PathValue("subjectId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *admin) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteClient(r.Context(), r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
