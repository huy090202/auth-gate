package consoletest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

// PortalKey is the operator key of the PortalFake.
const PortalKey = "harness-operator-key"

// FakeTenant is a workspace in the PortalFake, in the Portal's own JSON shape.
type FakeTenant struct {
	TenantID     string `json:"tenantId"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	CreatedAt    string `json:"createdAt"`
	MemberCount  int    `json:"memberCount"`
	CreatorEmail string `json:"creatorEmail"`
	SuspendedAt  string `json:"suspendedAt,omitempty"`
	DeletingAt   string `json:"deletingAt,omitempty"`

	Members        []FakeMember `json:"-"`
	PendingInvites int          `json:"-"`
}

// FakeMember is one member of a FakeTenant.
type FakeMember struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	JoinedAt    string `json:"joinedAt"`
}

// FakeUser is an account in the PortalFake, in the Portal's own JSON shape.
type FakeUser struct {
	UserID          string          `json:"userId"`
	Email           string          `json:"email"`
	DisplayName     string          `json:"displayName"`
	FactorCount     int             `json:"factorCount"`
	Tenants         []FakeTenantRef `json:"tenants"`
	EmailVerifiedAt string          `json:"emailVerifiedAt,omitempty"`
	SuspendedAt     string          `json:"suspendedAt,omitempty"`
	LastSignInAt    string          `json:"lastSignInAt,omitempty"`
}

// FakeTenantRef is a tenant in a user's view.
type FakeTenantRef struct {
	TenantID string `json:"tenantId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Role     string `json:"role"`
}

// PortalFake is a stand-in for the Portal's /api/operator/* routes with in-memory
// state. It checks the key and the JSON content type like the real one. SetDown makes
// every route answer 503.
type PortalFake struct {
	Server *httptest.Server

	mu      sync.Mutex
	tenants []*FakeTenant // oldest first; listed newest first
	users   []*FakeUser
	audit   map[string][]map[string]any // tenant id -> entries, newest last
	calls   []string
	down    bool
}

// AddTenant stores a tenant, giving it an id and a creation time when it has none.
func (p *PortalFake) AddTenant(t FakeTenant) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t.TenantID == "" {
		t.TenantID = id.New()
	}
	if t.CreatedAt == "" {
		t.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if t.Kind == "" {
		t.Kind = "org"
	}
	if t.MemberCount == 0 {
		t.MemberCount = len(t.Members)
	}
	p.tenants = append(p.tenants, &t)
	return t.TenantID
}

// AddUser stores a user, giving it an id when it has none.
func (p *PortalFake) AddUser(u FakeUser) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u.UserID == "" {
		u.UserID = id.New()
	}
	if u.Tenants == nil {
		u.Tenants = []FakeTenantRef{}
	}
	p.users = append(p.users, &u)
	return u.UserID
}

// SetDown makes every operator route answer 503 (true) or work again (false).
func (p *PortalFake) SetDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
}

// Calls returns every request received, as "METHOD /path", oldest first.
func (p *PortalFake) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// Tenant returns a copy of a stored tenant.
func (p *PortalFake) Tenant(tid string) (FakeTenant, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.tenants {
		if t.TenantID == tid {
			return *t, true
		}
	}
	return FakeTenant{}, false
}

// User returns a copy of a stored user.
func (p *PortalFake) User(uid string) (FakeUser, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.users {
		if u.UserID == uid {
			return *u, true
		}
	}
	return FakeUser{}, false
}

// TenantAuditEntries returns the audit entries the Portal was told to write for a tenant.
func (p *PortalFake) TenantAuditEntries(tid string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.audit[tid])
}

func newPortalFake(t *testing.T) *PortalFake {
	p := &PortalFake{audit: map[string][]map[string]any{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Server.Close)
	return p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (p *PortalFake) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("x-operator-key") != PortalKey {
		fail(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if p.down {
		fail(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/operator/tenants", p.listTenants)
	mux.HandleFunc("GET /api/operator/tenants/{tid}", p.getTenant)
	mux.HandleFunc("GET /api/operator/tenants/{tid}/audit", p.tenantAudit)
	mux.HandleFunc("POST /api/operator/tenants/{tid}/suspend", p.setTenantSuspended(true))
	mux.HandleFunc("POST /api/operator/tenants/{tid}/unsuspend", p.setTenantSuspended(false))
	mux.HandleFunc("POST /api/operator/tenants/{tid}/audit", p.quotaAudit)
	mux.HandleFunc("GET /api/operator/users", p.findUser)
	mux.HandleFunc("POST /api/operator/users/{uid}/suspend", p.setUserSuspended(true))
	mux.HandleFunc("POST /api/operator/users/{uid}/unsuspend", p.setUserSuspended(false))
	mux.HandleFunc("POST /api/operator/users/{uid}/resend-verification", p.resendVerification)
	mux.HandleFunc("POST /api/operator/users/{uid}/factor-reset", p.factorReset)
	mux.ServeHTTP(w, r)
}

func (p *PortalFake) tenant(r *http.Request) *FakeTenant {
	for _, t := range p.tenants {
		if t.TenantID == r.PathValue("tid") {
			return t
		}
	}
	return nil
}

func (p *PortalFake) user(r *http.Request) *FakeUser {
	for _, u := range p.users {
		if u.UserID == r.PathValue("uid") {
			return u
		}
	}
	return nil
}

func (p *PortalFake) listTenants(w http.ResponseWriter, _ *http.Request) {
	out := make([]FakeTenant, 0, len(p.tenants))
	for _, t := range slices.Backward(p.tenants) {
		out = append(out, *t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
}

func (p *PortalFake) getTenant(w http.ResponseWriter, r *http.Request) {
	t := p.tenant(r)
	if t == nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	members := t.Members
	if members == nil {
		members = []FakeMember{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t, "members": members, "pendingInvites": t.PendingInvites})
}

func (p *PortalFake) tenantAudit(w http.ResponseWriter, r *http.Request) {
	t := p.tenant(r)
	if t == nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	entries := []map[string]any{}
	for _, e := range slices.Backward(p.audit[t.TenantID]) {
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (p *PortalFake) auditAdd(tid, action string, detail map[string]string) {
	e := map[string]any{"actorId": "operator", "actorName": "AuthGate administrator", "action": action, "target": tid, "result": "success",
		"at": time.Now().UTC().Format(time.RFC3339Nano)}
	if detail != nil {
		e["detail"] = detail
	}
	p.audit[tid] = append(p.audit[tid], e)
}

func (p *PortalFake) setTenantSuspended(suspend bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := p.tenant(r)
		if t == nil {
			fail(w, http.StatusNotFound, "not_found")
			return
		}
		if (t.SuspendedAt != "") != suspend {
			t.SuspendedAt = ""
			action := "tenant-unsuspend"
			if suspend {
				t.SuspendedAt = time.Now().UTC().Format(time.RFC3339Nano)
				action = "tenant-suspend"
			}
			p.auditAdd(t.TenantID, action, nil)
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func (p *PortalFake) quotaAudit(w http.ResponseWriter, r *http.Request) {
	t := p.tenant(r)
	if t == nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	var in struct {
		Action   string
		From, To *int
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Action != "tenant-quota" || in.From == nil || in.To == nil {
		fail(w, http.StatusBadRequest, "invalid_action")
		return
	}
	p.auditAdd(t.TenantID, in.Action, map[string]string{"from": strconv.Itoa(*in.From), "to": strconv.Itoa(*in.To)})
	w.WriteHeader(http.StatusNoContent)
}

func (p *PortalFake) findUser(w http.ResponseWriter, r *http.Request) {
	for _, u := range p.users {
		if u.Email == r.URL.Query().Get("email") {
			writeJSON(w, http.StatusOK, u)
			return
		}
	}
	fail(w, http.StatusNotFound, "not_found")
}

func (p *PortalFake) setUserSuspended(suspend bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := p.user(r)
		if u == nil {
			fail(w, http.StatusNotFound, "not_found")
			return
		}
		u.SuspendedAt = ""
		if suspend {
			u.SuspendedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		writeJSON(w, http.StatusOK, u)
	}
}

func (p *PortalFake) resendVerification(w http.ResponseWriter, r *http.Request) {
	u := p.user(r)
	switch {
	case u == nil:
		fail(w, http.StatusNotFound, "not_found")
	case u.EmailVerifiedAt != "":
		fail(w, http.StatusConflict, "already_verified")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	}
}

func (p *PortalFake) factorReset(w http.ResponseWriter, r *http.Request) {
	if p.user(r) == nil {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"dueAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)})
}
