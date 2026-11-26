package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/avatar"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// DevAvatarUploadPath is where the dev server receives avatar uploads in place of S3.
const DevAvatarUploadPath = "/dev/avatars-upload"

// DevRoutes wraps the Console handler with the two routes only the dev server
// has (see avatar.DevRoutes). They sit outside New's wrapping because an upload
// is not JSON. main registers them only with CONSOLE_DEV=1, so a deployed
// Console has neither.
func DevRoutes(next http.Handler, root string, dir *avatar.Dir) http.Handler {
	mux := http.NewServeMux()
	avatar.DevRoutes(mux, root, dir, DevAvatarUploadPath)
	mux.Handle("/", next)
	return mux
}

// avatarTypes maps the allowed content types to the extension in the key, and
// back (avatarExts).
var avatarTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}
var avatarExts = map[string]string{"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp"}

var errInvalidAvatar = &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_avatar"}

// Uploads land under pendingPrefix, which CloudFront does not serve and a
// lifecycle rule expires after a day; a confirm moves one under avatarPrefix,
// the only prefix /avatars/* serves.
const (
	pendingPrefix = "pending/"
	avatarPrefix  = "avatars/"
	// sideEffectLimit bounds the cleanup of a stray object after the request.
	sideEffectLimit = 10 * time.Second
)

func (a *App) avatarForm(w http.ResponseWriter, r *http.Request) error {
	_, admin := sessionFrom(r.Context())
	var in struct{ ContentType string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ext, ok := avatarTypes[in.ContentType]
	if !ok {
		return errInvalidAvatar
	}
	key := pendingPrefix + admin.ID + "/" + id.New() + "." + ext
	f, err := a.Avatars.PresignPost(r.Context(), key, in.ContentType)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"url": f.URL, "fields": f.Fields, "key": key})
	return nil
}

// ownPendingAvatar is, when key is exactly pending/<adminId>/<ulid>.<ext>, the
// part after pending/ (the key's name under avatars/) and the extension.
func ownPendingAvatar(adminID, key string) (name, ext string, ok bool) {
	name, ok = strings.CutPrefix(key, pendingPrefix)
	if !ok {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(name, adminID+"/")
	if !ok {
		return "", "", false
	}
	file, ext, ok := strings.Cut(rest, ".")
	if !ok || id.ValidateULID(file) != nil || avatarExts[ext] == "" {
		return "", "", false
	}
	return name, ext, true
}

// confirmAvatar accepts the caller's pending upload as the avatar once S3
// reports a size and type the form should have allowed: it is copied under
// avatars/, the pending object is deleted, and only then is it set on the
// account. An object that is not as promised is deleted. A key that is not the
// caller's own pending upload is refused without touching it.
func (a *App) confirmAvatar(w http.ResponseWriter, r *http.Request) error {
	_, admin := sessionFrom(r.Context())
	ctx := r.Context()
	var in struct{ Key string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	name, ext, ok := ownPendingAvatar(admin.ID, in.Key)
	if !ok {
		return errInvalidAvatar
	}
	size, ct, err := a.Avatars.Head(ctx, in.Key)
	if err != nil {
		slog.Warn("console: avatar not found at confirm", "err", err)
		return errInvalidAvatar
	}
	if size < 1 || size > avatar.MaxBytes || ct != avatarExts[ext] {
		if err := a.Avatars.Delete(ctx, in.Key); err != nil {
			slog.Warn("console: refused avatar not deleted", "err", err)
		}
		return errInvalidAvatar
	}
	key := avatarPrefix + name
	if err := a.Avatars.Copy(ctx, in.Key, key); err != nil {
		return fmt.Errorf("avatar copy: %w", err)
	}
	if err := a.Avatars.Delete(ctx, in.Key); err != nil {
		slog.Warn("console: confirmed avatar's upload not deleted; it expires in a day", "err", err)
	}
	prev, err := a.Store.swapAvatar(ctx, admin.Username, key)
	if err != nil {
		a.dropAvatar(ctx, key, "")
		return adminGone(err)
	}
	a.dropAvatar(ctx, prev, key)
	setAuditTarget(ctx, key)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"avatarUrl": "/" + key})
	return nil
}

func (a *App) removeAvatar(w http.ResponseWriter, r *http.Request) error {
	_, admin := sessionFrom(r.Context())
	prev, err := a.Store.swapAvatar(r.Context(), admin.Username, "")
	if err != nil {
		return adminGone(err)
	}
	a.dropAvatar(r.Context(), prev, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// adminGone answers a missing account as a lost session.
func adminGone(err error) error {
	if errors.Is(err, ErrAdminNotFound) {
		return errUnauthorized
	}
	return err
}

// dropAvatar deletes an avatar object that is no longer used, unless it is keep.
// A failure is logged: the object is only a stray file.
func (a *App) dropAvatar(ctx context.Context, key, keep string) {
	if key == "" || key == keep || a.Avatars == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectLimit)
	defer cancel()
	if err := a.Avatars.Delete(ctx, key); err != nil {
		slog.Warn("console: old avatar not deleted", "err", err)
	}
}
