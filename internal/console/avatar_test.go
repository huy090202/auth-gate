package console_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
	"github.com/huy090202/auth-gate/internal/platform/avatar"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

type memObj struct {
	size int64
	ct   string
}

// memAvatars is an in-memory avatar store.
type memAvatars struct {
	objs    map[string]memObj
	deleted []string
}

func (m *memAvatars) PresignPost(_ context.Context, key, ct string) (avatar.PostForm, error) {
	return avatar.PostForm{URL: "https://s3.test/up", Fields: map[string]string{"key": key, "Content-Type": ct}}, nil
}
func (m *memAvatars) Head(_ context.Context, key string) (int64, string, error) {
	o, ok := m.objs[key]
	if !ok {
		return 0, "", errors.New("no such object")
	}
	return o.size, o.ct, nil
}
func (m *memAvatars) Delete(_ context.Context, key string) error {
	delete(m.objs, key)
	m.deleted = append(m.deleted, key)
	return nil
}
func (m *memAvatars) Copy(_ context.Context, from, to string) error {
	o, ok := m.objs[from]
	if !ok {
		return errors.New("no such object")
	}
	m.objs[to] = o
	return nil
}

// Why: one admin must not set an image another uploaded.
func TestAvatarOnlyOwnPending(t *testing.T) {
	h := consoletest.New(t)
	mem := &memAvatars{objs: map[string]memObj{}}
	d := h.Deps
	d.Avatars = mem
	b, a := fullAdmin(t, h, "ada", console.RoleAdmin)
	b2 := h.BrowserFor(console.New(d))
	b2.SetCookie(b.CookieValue())
	confirm := func(key string) consoletest.Response {
		return b2.Do("POST", "/api/me/avatar/confirm", map[string]string{"key": key})
	}

	want(t, b2.Do("POST", "/api/me/avatar", map[string]string{"contentType": "image/gif"}), 400, "invalid_avatar")
	r := b2.Do("POST", "/api/me/avatar", map[string]string{"contentType": "image/png"})
	want(t, r, 200, "")
	own := r.Str("key")
	if !strings.HasPrefix(own, "pending/"+a.ID+"/") {
		t.Fatalf("key = %q", own)
	}

	// Another admin's pending upload, and an already confirmed key, are refused untouched.
	other := "pending/" + id.New() + "/" + id.New() + ".png"
	served := "avatars/" + a.ID + "/" + id.New() + ".png"
	for _, k := range []string{other, served} {
		mem.objs[k] = memObj{10, "image/png"}
		want(t, confirm(k), 400, "invalid_avatar")
	}
	if len(mem.deleted) != 0 || len(mem.objs) != 2 {
		t.Fatalf("a refused key was touched: %v, deleted %v", mem.objs, mem.deleted)
	}

	// Over 1 MB is refused and deleted.
	mem.objs[own] = memObj{avatar.MaxBytes + 1, "image/png"}
	want(t, confirm(own), 400, "invalid_avatar")
	if _, ok := mem.objs[own]; ok {
		t.Fatal("the oversize upload was kept")
	}
	got, err := h.Store().GetAdmin(context.Background(), "ada")
	if err != nil || got.AvatarKey != "" {
		t.Fatalf("avatar after refusals = %q (%v)", got.AvatarKey, err)
	}

	// The happy path: confirm, /api/me shows it, a second confirm drops the first, remove clears it.
	set := func() string {
		r := b2.Do("POST", "/api/me/avatar", map[string]string{"contentType": "image/png"})
		want(t, r, 200, "")
		mem.objs[r.Str("key")] = memObj{100, "image/png"}
		c := confirm(r.Str("key"))
		want(t, c, 200, "")
		return c.Str("avatarUrl")
	}
	first := set()
	if !strings.HasPrefix(first, "/avatars/"+a.ID+"/") {
		t.Fatalf("avatarUrl = %q", first)
	}
	if got := b2.Do("GET", "/api/me", nil).Str("avatarUrl"); got != first {
		t.Fatalf("me avatarUrl = %q", got)
	}
	second := set()
	if _, ok := mem.objs[strings.TrimPrefix(first, "/")]; ok || second == first {
		t.Fatal("the replaced avatar was kept")
	}
	want(t, b2.Do("DELETE", "/api/me/avatar", nil), 204, "")
	if _, ok := mem.objs[strings.TrimPrefix(second, "/")]; ok {
		t.Fatal("the removed avatar was kept")
	}
	if got := b2.Do("GET", "/api/me", nil).Map()["avatarUrl"]; got != nil {
		t.Fatalf("me avatarUrl after remove = %v", got)
	}
}
