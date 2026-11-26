package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/avatar"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

const (
	emailChangeLife = 24 * time.Hour
	// maxClientPages bounds the walk over a workspace's clients, so a cursor that
	// never ends cannot hold a deletion forever.
	maxClientPages = 1000
)

// avatarTypes maps the allowed content types to the extension in the key, and
// back (avatarExts).
var avatarTypes = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}
var avatarExts = map[string]string{"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp"}

// ---- store ----

// SetDisplayName stores a new display name.
func (st *Store) SetDisplayName(ctx context.Context, userID, name string) error {
	err := st.update(ctx, userID, "SET displayName = :n", exists, nil, item{":n": s(name)})
	return st.mapNotFound(err, "set display name")
}

// SetPasswordAtEpoch stores a new password hash and moves sessionEpoch up by one,
// if the epoch is still the one read. Of two changes holding the same epoch only
// one wins; the other gets ErrConflict.
func (st *Store) SetPasswordAtEpoch(ctx context.Context, userID, hash string, epoch int) error {
	err := st.update(ctx, userID, "SET passwordHash = :h ADD sessionEpoch :one", epochCond(epoch), nil,
		item{":h": s(hash), ":one": n(1), ":e": n(int64(epoch))})
	if dynamo.IsConditionFailed(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return nil
}

// ChangeEmail moves the user's address from oldEmail to newEmail in one
// transaction: EMAIL#old goes, EMAIL#new is written, the user's email is
// updated and a pending factor reset is cancelled (it was confirmed from the
// old mailbox). A new address held by another account gives ErrEmailTaken (an
// unverified holder past its ttl, not yet reaped, does not count); an old
// address that is no longer the user's gives ErrConflict.
func (st *Store) ChangeEmail(ctx context.Context, userID, oldEmail, newEmail string) error {
	now := st.now()
	ei := emailKey(newEmail)
	ei["userId"] = s(userID)
	err := dynamo.TransactWrite(ctx, st.DB,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(st.Table), Key: emailKey(oldEmail),
			ConditionExpression:       aws.String(exists + " AND userId = :u"),
			ExpressionAttributeValues: item{":u": s(userID)},
		}},
		types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(st.Table), Item: ei,
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR #ttl <= :now"),
			ExpressionAttributeNames:  map[string]string{"#ttl": "ttl"},
			ExpressionAttributeValues: item{":now": n(now.Unix())},
		}},
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: userKey(userID),
			UpdateExpression:          aws.String("SET email = :new"),
			ConditionExpression:       aws.String(exists + " AND email = :old"),
			ExpressionAttributeValues: item{":new": s(newEmail), ":old": s(oldEmail)},
		}},
		types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(st.Table), Key: factorResetKey(userID)}})
	var tx *types.TransactionCanceledException
	if errors.As(err, &tx) && len(tx.CancellationReasons) == 4 {
		switch {
		case aws.ToString(tx.CancellationReasons[1].Code) == "ConditionalCheckFailed":
			return ErrEmailTaken
		case dynamo.IsConditionFailed(err):
			return ErrConflict
		}
	}
	if err != nil {
		return fmt.Errorf("change email: %w", err)
	}
	return nil
}

// swapAvatar sets the user's avatar key (empty removes it) and returns the one it replaced.
func (st *Store) swapAvatar(ctx context.Context, userID, key string) (prev string, err error) {
	expr := "SET avatarKey = :k"
	vals := item{":k": s(key)}
	if key == "" {
		expr, vals = "REMOVE avatarKey", nil
	}
	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: userKey(userID), UpdateExpression: aws.String(expr),
		ConditionExpression: aws.String(exists), ReturnValues: types.ReturnValueAllOld,
	}
	if vals != nil {
		in.ExpressionAttributeValues = vals
	}
	out, err := st.DB.UpdateItem(ctx, in)
	if err = st.mapNotFound(err, "set avatar"); err != nil {
		return "", err
	}
	return getS(out.Attributes, "avatarKey"), nil
}

// DeleteAccount removes the user and everything the Portal keeps for it: the
// email item, the memberships (both sides), the personal workspace and its
// index entry, the recovery codes, a pending factor reset and, last and without
// a guarantee, the workspace's audit log. The items go in one transaction when
// they fit in one, with the user and email items in the last. The user's email
// must still be u.Email, else ErrConflict.
//
// The caller has already left or deleted every organisation. A membership
// other than the personal workspace's is one accepted since then, which only
// RemoveMember may undo with its counters, so it gives ErrConflict and the
// caller sorts the organisations again. An invite accepted after the list
// raises the user's membershipVersion, which the user item's delete is
// conditioned on: that too gives ErrConflict.
func (st *Store) DeleteAccount(ctx context.Context, u User) error {
	del := func(k item, cond string, vals item) types.TransactWriteItem {
		d := &types.Delete{TableName: aws.String(st.Table), Key: k}
		if cond != "" {
			d.ConditionExpression, d.ExpressionAttributeValues = aws.String(cond), vals
		}
		return types.TransactWriteItem{Delete: d}
	}
	cur, err := st.getRaw(ctx, userKey(u.ID))
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrConflict
	}
	ms, err := st.ListMemberships(ctx, u.ID)
	if err != nil {
		return err
	}
	var core []types.TransactWriteItem
	for _, m := range ms {
		if m.TenantID != u.PersonalTenantID {
			return ErrConflict
		}
		core = append(core, del(memberKey(m.TenantID, u.ID), "", nil), del(userTenantKey(u.ID, m.TenantID), "", nil))
	}
	if t := u.PersonalTenantID; t != "" {
		core = append(core, del(tenantKey(t), "", nil), del(tenantIndexKey(t), "", nil))
	}
	version := getN64(cur, "membershipVersion") // absent is 0
	userCond, userVals := "email = :e AND membershipVersion = :v", item{":e": s(u.Email), ":v": n(version)}
	if version == 0 {
		userCond = "email = :e AND (attribute_not_exists(membershipVersion) OR membershipVersion = :v)"
	}
	core = append(core,
		del(emailKey(u.Email), "userId = :u", item{":u": s(u.ID)}),
		del(userKey(u.ID), userCond, userVals))
	extra := []types.TransactWriteItem{del(factorResetKey(u.ID), "", nil)}
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :r)"),
			ExpressionAttributeValues: item{":p": s("USER#" + u.ID), ":r": s("RECOVERY#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return fmt.Errorf("list recovery codes: %w", err)
		}
		for _, it := range out.Items {
			extra = append(extra, del(item{"pk": it["pk"], "sk": it["sk"]}, "", nil))
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	const txMax = 100
	for len(core)+len(extra) > txMax {
		n := min(len(extra), txMax)
		if err := dynamo.TransactWrite(ctx, st.DB, extra[:n]...); err != nil {
			return err
		}
		extra = extra[n:]
	}
	err = dynamo.TransactWrite(ctx, st.DB, append(extra, core...)...)
	if dynamo.IsConditionFailed(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if t := u.PersonalTenantID; t != "" {
		st.deleteAudit(ctx, t)
	}
	return nil
}

// deleteAudit removes a deleted workspace's audit entries. It is best effort:
// the entries expire by themselves after 180 days.
func (st *Store) deleteAudit(ctx context.Context, tenantID string) {
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true), ProjectionExpression: aws.String("pk, sk"),
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :a)"),
			ExpressionAttributeValues: item{":p": s("TENANT#" + tenantID), ":a": s("AUDIT#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			slog.Warn("portal: audit entries of a deleted account not removed", "err", err)
			return
		}
		for _, it := range out.Items {
			if _, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(st.Table), Key: it}); err != nil {
				slog.Warn("portal: audit entry of a deleted account not removed", "err", err)
				return
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return
		}
		start = out.LastEvaluatedKey
	}
}

// ---- routes ----

func (a *App) mountProfile(mux *http.ServeMux) {
	self := a.session(StageEnroll, StageFull)
	full := a.session(StageFull)
	h := func(name string, stage func(http.Handler) http.Handler, f func(http.ResponseWriter, *http.Request) error) http.Handler {
		return a.audited(name, stage(a.account(webapi.HandlerFunc(f))))
	}
	mux.Handle("POST /api/me/display-name", h("display-name", self, a.setDisplayName))
	mux.Handle("POST /api/me/password", h("password-change", self, a.changePassword))
	mux.Handle("POST /api/me/email", h("email-change-requested", full, a.requestEmailChange))
	mux.Handle("POST /api/me/avatar", full(webapi.HandlerFunc(a.avatarForm)))
	mux.Handle("POST /api/me/avatar/confirm", h("avatar-set", full, a.confirmAvatar))
	mux.Handle("DELETE /api/me/avatar", h("avatar-removed", full, a.removeAvatar))
	// The tenant is deleted with the account, so its log is not written to: the
	// action goes to the process log (no audit scope is set).
	mux.Handle("DELETE /api/me", a.audited("account-deleted", full(webapi.HandlerFunc(a.deleteAccount))))
	mux.Handle("POST /api/auth/confirm-email", a.audited("email-changed", webapi.HandlerFunc(a.confirmEmailChange)))
}

func (a *App) setDisplayName(w http.ResponseWriter, r *http.Request) error {
	_, u := sessionFrom(r.Context())
	var in struct{ DisplayName string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	name, ok := validName(in.DisplayName)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_display_name")
	}
	if err := a.Store.SetDisplayName(r.Context(), u.ID, name); err != nil {
		return sessionUserGone(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"displayName": name})
	return nil
}

func sessionUserGone(err error) error {
	if errors.Is(err, ErrUserNotFound) {
		return errUnauthorized
	}
	return err
}

// changePassword checks the current password, sets the new one and ends every
// other session; this one is reissued at the new epoch so the person stays in.
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	ctx := r.Context()
	tok, err := a.checkStepUp(r, x, "change-password", "")
	if err != nil {
		return err
	}
	var in struct{ CurrentPassword, NewPassword string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if !a.verifyPassword(in.CurrentPassword, u.PasswordHash) {
		// Without a step-up token to stand in the way, guesses count like sign-in failures.
		if x.Stage == StageEnroll {
			if err := a.Store.RecordLoginFailure(ctx, u.ID, a.now()); err != nil {
				return sessionUserGone(err)
			}
			cur, err := a.Store.GetUser(ctx, u.ID)
			if err != nil {
				return sessionUserGone(err)
			}
			if cur.LockedUntil != nil && a.now().Before(*cur.LockedUntil) {
				return a.endSession(w, r, x)
			}
		}
		return apiErr(http.StatusBadRequest, "wrong_password")
	}
	// The current password has just been verified, so equal strings mean "same as current".
	if in.NewPassword == in.CurrentPassword || password.Validate(in.NewPassword) != nil {
		return apiErr(http.StatusBadRequest, "invalid_password")
	}
	switch breached, err := a.Passwords.Breached(ctx, in.NewPassword); {
	case err != nil:
		slog.Warn("portal: breached-password check failed; letting the password through", "err", err)
	case breached:
		return apiErr(http.StatusBadRequest, "password_breached")
	}
	hash, err := password.Hash(in.NewPassword)
	if err != nil {
		return err
	}
	if _, err := a.factorsForWrite(w, r, x); err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.Store.SetPasswordAtEpoch(ctx, u.ID, hash, u.SessionEpoch); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrUserNotFound) {
			return errUnauthorized
		}
		return err
	}
	next := x.Stage
	if x.Stage == StageEnroll && x.FactorProved {
		next = StageFull
	}
	if _, err := a.reissue(ctx, w, x, next, u.SessionEpoch+1); err != nil {
		return err
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "password-changed"})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": next})
	return nil
}

// requestEmailChange mails a link to the new address; nothing changes until it
// is opened. An address that already has an account answers the same 202 and
// sends nothing, so the answer does not say which addresses are taken.
func (a *App) requestEmailChange(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	ctx := r.Context()
	tok, err := a.checkStepUp(r, x, "change-email", "")
	if err != nil {
		return err
	}
	var in struct{ NewEmail string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	email, ok := parseEmail(in.NewEmail)
	if !ok || email == u.Email {
		return apiErr(http.StatusBadRequest, "invalid_email")
	}
	if err := a.limitEmail(ctx, email); err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	switch _, err := a.Store.GetUserByEmail(ctx, email); {
	case errors.Is(err, ErrUserNotFound):
		token, hash, err := a.newToken()
		if err != nil {
			return err
		}
		if err := a.Store.PutToken(ctx, Token{Hash: hash, Purpose: PurposeEmailChange, UserID: u.ID, Email: email,
			ExpiresAt: a.now().Add(emailChangeLife), Epoch: u.SessionEpoch}); err != nil {
			return err
		}
		a.mailBestEffort(ctx, pmail.Message{To: email, Lang: u.Lang, Kind: "email-change-confirm",
			Vars: map[string]string{"link": a.Config.PublicBaseURL + "/confirm-email#token=" + token}})
	case err != nil:
		return err
	}
	httpx.WriteJSON(w, http.StatusAccepted, checkEmail())
	return nil
}

// confirmEmailChange spends the link and moves the address. The link is bound to
// the epoch it was issued at, so a password change or reset voids it.
func (a *App) confirmEmailChange(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Token string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if err := a.spendIP(r, "recover-ip", recoverIPLimit); err != nil {
		return err
	}
	if in.Token == "" || len(in.Token) > 128 {
		return apiErr(http.StatusBadRequest, "invalid_token")
	}
	ctx := r.Context()
	invalid := apiErr(http.StatusBadRequest, "invalid_token")
	t, err := a.Store.UseToken(ctx, PurposeEmailChange, hashToken(in.Token), a.now())
	if errors.Is(err, ErrTokenInvalid) {
		return invalid
	}
	if err != nil {
		return err
	}
	u, err := a.Store.GetUser(ctx, t.UserID)
	if errors.Is(err, ErrUserNotFound) || (err == nil && (!mailable(u) || u.SessionEpoch != t.Epoch)) || t.Email == "" {
		return invalid
	}
	if err != nil {
		return err
	}
	setAuditActor(ctx, u.ID)
	setAuditTenant(ctx, u.PersonalTenantID)
	switch err := a.Store.ChangeEmail(ctx, u.ID, u.Email, t.Email); {
	case errors.Is(err, ErrEmailTaken):
		return apiErr(http.StatusConflict, "email_taken")
	case errors.Is(err, ErrConflict):
		return invalid
	case err != nil:
		return err
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "email-changed"})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- avatar ----

var errInvalidAvatar = apiErr(http.StatusBadRequest, "invalid_avatar")

// Uploads land under pendingPrefix, which CloudFront does not serve and a
// lifecycle rule expires after a day; a confirm moves one under avatarPrefix,
// the only prefix /avatars/* serves.
const (
	pendingPrefix = "pending/"
	avatarPrefix  = "avatars/"
)

func (a *App) avatarForm(w http.ResponseWriter, r *http.Request) error {
	_, u := sessionFrom(r.Context())
	var in struct{ ContentType string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ext, ok := avatarTypes[in.ContentType]
	if !ok {
		return errInvalidAvatar
	}
	key := pendingPrefix + u.ID + "/" + id.New() + "." + ext
	f, err := a.Avatars.PresignPost(r.Context(), key, in.ContentType)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"url": f.URL, "fields": f.Fields, "key": key})
	return nil
}

// ownPendingAvatar is, when key is exactly pending/<userId>/<ulid>.<ext>, the
// part after pending/ (the key's name under avatars/) and the extension.
func ownPendingAvatar(userID, key string) (name, ext string, ok bool) {
	name, ok = strings.CutPrefix(key, pendingPrefix)
	if !ok {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(name, userID+"/")
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
	_, u := sessionFrom(r.Context())
	ctx := r.Context()
	var in struct{ Key string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	name, ext, ok := ownPendingAvatar(u.ID, in.Key)
	if !ok {
		return errInvalidAvatar
	}
	size, ct, err := a.Avatars.Head(ctx, in.Key)
	if err != nil {
		slog.Warn("portal: avatar not found at confirm", "err", err)
		return errInvalidAvatar
	}
	if size < 1 || size > avatar.MaxBytes || ct != avatarExts[ext] {
		if err := a.Avatars.Delete(ctx, in.Key); err != nil {
			slog.Warn("portal: refused avatar not deleted", "err", err)
		}
		return errInvalidAvatar
	}
	key := avatarPrefix + name
	if err := a.Avatars.Copy(ctx, in.Key, key); err != nil {
		return fmt.Errorf("avatar copy: %w", err)
	}
	if err := a.Avatars.Delete(ctx, in.Key); err != nil {
		slog.Warn("portal: confirmed avatar's upload not deleted; it expires in a day", "err", err)
	}
	prev, err := a.Store.swapAvatar(ctx, u.ID, key)
	if err != nil {
		a.dropAvatar(ctx, key, "")
		return sessionUserGone(err)
	}
	a.dropAvatar(ctx, prev, key)
	setAuditTarget(ctx, key)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"avatarUrl": "/" + key})
	return nil
}

func (a *App) removeAvatar(w http.ResponseWriter, r *http.Request) error {
	_, u := sessionFrom(r.Context())
	prev, err := a.Store.swapAvatar(r.Context(), u.ID, "")
	if err != nil {
		return sessionUserGone(err)
	}
	a.dropAvatar(r.Context(), prev, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
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
		slog.Warn("portal: old avatar not deleted", "err", err)
	}
}

// ---- account deletion ----

// workspaceClients lists the ids of every client of the workspace, page by page.
func (a *App) workspaceClients(ctx context.Context, tenantID string) ([]string, error) {
	var ids []string
	cursor := ""
	for range maxClientPages {
		raw, err := a.AuthGate.Tenant(tenantID).List(ctx, cursor)
		if err != nil {
			return nil, err
		}
		var page struct {
			Clients []struct {
				ClientID string `json:"clientId"`
			} `json:"clients"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("%w: unreadable client list", authgate.ErrUnavailable)
		}
		for _, c := range page.Clients {
			ids = append(ids, c.ClientID)
		}
		if page.NextCursor == "" {
			return ids, nil
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("%w: client list does not end", authgate.ErrUnavailable)
}

var errAuthGateDown = apiErr(http.StatusBadGateway, "authgate_unavailable")

// accountOrgs is what account deletion does with each organisation the person
// belongs to.
type accountOrgs struct {
	soleOwner []string     // names of those they alone own, with other members: deletion is refused
	remove    []Tenant     // those they are the only member of, or already being deleted
	leave     []Membership // the others: their membership is removed
	gone      []string     // tenants whose item is gone: the leftover membership is dropped
}

// classifyOrgs sorts the person's organisations for account deletion (spec section 4).
func (a *App) classifyOrgs(ctx context.Context, userID string) (accountOrgs, error) {
	var out accountOrgs
	ms, err := a.Store.ListMemberships(ctx, userID)
	if err != nil {
		return out, err
	}
	for _, m := range ms {
		t, err := a.Store.GetTenant(ctx, m.TenantID)
		if errors.Is(err, ErrTenantNotFound) {
			out.gone = append(out.gone, m.TenantID)
			continue
		}
		if err != nil {
			return out, err
		}
		switch {
		case t.Kind != KindOrg:
		case t.DeletingAt != nil:
			out.remove = append(out.remove, t) // finishing it drops this membership too
		case m.Role == RoleOwner && t.Counts.Owners == 1 && t.Counts.Members > 1:
			out.soleOwner = append(out.soleOwner, t.Name)
		case m.Role == RoleOwner && t.Counts.Members == 1:
			out.remove = append(out.remove, t)
		default:
			out.leave = append(out.leave, m)
		}
	}
	return out, nil
}

// deleteAccount deletes the person's workspace clients at AuthGate, the
// organisations they are the only member of, their place in the others, their
// own factors, then everything of theirs in the Portal. Being the only owner of
// an organisation that has other members refuses it with 409 sole_owner, before
// the step-up is spent, so the person can transfer ownership and retry with the
// same token. A failed AuthGate call on the way stops it with 502 and the
// account whole, so the person can run it again (with a new step-up); an
// organisation it had started to delete stays marked and is finished then.
func (a *App) deleteAccount(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	ctx := r.Context()
	tok, err := a.checkStepUp(r, x, "delete-account", "")
	if err != nil {
		return err
	}
	orgs, err := a.classifyOrgs(ctx, u.ID)
	if err != nil {
		return err
	}
	if len(orgs.soleOwner) > 0 {
		return &webapi.Error{Status: http.StatusConflict, Code: "sole_owner", Extra: map[string]any{"organisations": orgs.soleOwner}}
	}
	var clients []string
	if u.PersonalTenantID != "" {
		if clients, err = a.workspaceClients(ctx, u.PersonalTenantID); err != nil {
			slog.Warn("portal: account deletion: clients not listed", "err", err)
			return errAuthGateDown
		}
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.deleteClients(ctx, u.PersonalTenantID, clients); err != nil {
		slog.Warn("portal: account deletion: client not deleted", "err", err)
		return errAuthGateDown
	}
	for _, t := range orgs.remove {
		marked, err := a.Store.MarkDeletingIfAlone(ctx, t.ID, a.now())
		if errors.Is(err, ErrTenantNotFound) {
			orgs.gone = append(orgs.gone, t.ID)
			continue
		}
		if errors.Is(err, ErrNotAlone) {
			// Someone joined since classifyOrgs: the person is now its sole owner with members.
			return &webapi.Error{Status: http.StatusConflict, Code: "sole_owner", Extra: map[string]any{"organisations": []string{t.Name}}}
		}
		if errors.Is(err, ErrConflict) {
			return apiErr(http.StatusConflict, "conflict")
		}
		if err != nil {
			return err
		}
		if t.DeletingAt == nil {
			a.auditOrgDelete(r, u, t.ID)
		}
		if err := a.finishOrgDeletion(ctx, marked, u.ID); err != nil {
			slog.Warn("portal: account deletion: organisation not deleted", "tenant", t.ID, "err", err)
			return errAuthGateDown
		}
	}
	for _, m := range orgs.leave {
		_, err := a.Store.RemoveMember(ctx, m.TenantID, m, m)
		switch {
		case err == nil, errors.Is(err, ErrTenantNotFound):
		case errors.Is(err, ErrLastOwner), errors.Is(err, ErrConflict), errors.Is(err, ErrTenantDeleting):
			return apiErr(http.StatusConflict, "conflict") // something moved since the check: the person may retry
		default:
			return err
		}
	}
	// Store.DeleteAccount refuses any membership but the personal one.
	for _, tid := range orgs.gone {
		if err := a.Store.DropMembership(ctx, tid, u.ID); err != nil {
			return err
		}
	}
	a.deleteOwnFactors(ctx, u.ID)
	if err := a.Store.DeleteAccount(ctx, u); err != nil {
		if errors.Is(err, ErrConflict) {
			return apiErr(http.StatusConflict, "conflict")
		}
		return err
	}
	slog.Info("portal: account deleted", "user", u.ID, "clients", len(clients))
	a.dropAvatar(ctx, u.AvatarKey, "")
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "account-deleted"})
	if err := a.Store.DeleteSession(ctx, x.Hash); err != nil {
		slog.Warn("portal: session of a deleted account not removed", "err", err)
	}
	a.guard().SetCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// deleteOwnFactors removes the user's PIN and passkeys at AuthGate. Best effort:
// a failure leaves factors of a subject that no longer exists, which nothing can use.
func (a *App) deleteOwnFactors(ctx context.Context, userID string) {
	f, err := a.AuthGate.Factors(ctx, userID)
	if err != nil {
		slog.Warn("portal: account deletion: factors not listed", "err", err)
		return
	}
	if f.PIN {
		if err := a.AuthGate.DeletePIN(ctx, userID); err != nil {
			slog.Warn("portal: account deletion: PIN not deleted", "err", err)
		}
	}
	for _, c := range f.WebAuthn {
		if err := a.AuthGate.DeleteCredential(ctx, userID, c.CredentialID); err != nil {
			slog.Warn("portal: account deletion: passkey not deleted", "err", err)
		}
	}
}
