package portal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

const (
	// recoveryAlphabet is Crockford's base32: no I, L, O or U.
	recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	recoveryCount    = 10
	recoveryLen      = 10 // characters, shown as XXXXX-XXXXX
)

// newRecoveryCodes makes ten random codes. Thirty-two symbols divide 256, so a
// byte's low five bits are uniform.
func newRecoveryCodes() ([]string, error) {
	codes := make([]string, recoveryCount)
	buf := make([]byte, recoveryLen)
	for i := range codes {
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("recovery code: %w", err)
		}
		raw := make([]byte, recoveryLen)
		for j, b := range buf {
			raw[j] = recoveryAlphabet[b&31]
		}
		codes[i] = string(raw[:5]) + "-" + string(raw[5:])
	}
	return codes, nil
}

// normaliseCode is the one spelling of a code used to hash and to compare:
// upper case, spaces and dashes dropped, then the dash put back in the middle.
// Anything that is not ten characters long keeps no dash, so it matches nothing.
func normaliseCode(s string) string {
	s = strings.ToUpper(s)
	s = strings.NewReplacer(" ", "", "-", "").Replace(s)
	if len(s) != recoveryLen {
		return s
	}
	return s[:5] + "-" + s[5:]
}

// codeHash is sha256 of the user's id, a bar and the normalised code, so two
// users who hold the same code (however unlikely) store different keys.
func codeHash(userID, normalised string) string {
	sum := sha256.Sum256([]byte(userID + "|" + normalised))
	return hex.EncodeToString(sum[:])
}

func recoveryKey(userID, hash string) item {
	return item{"pk": s("USER#" + userID), "sk": s("RECOVERY#" + hash)}
}

// ReplaceRecoveryCodes deletes the user's whole set and stores the hashes of
// codes (none to just delete) in one transaction, so a set is never half old
// and half new.
func (st *Store) ReplaceRecoveryCodes(ctx context.Context, userID string, codes []string) error {
	var ops []types.TransactWriteItem
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :r)"),
			ExpressionAttributeValues: item{":p": s("USER#" + userID), ":r": s("RECOVERY#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return fmt.Errorf("list recovery codes: %w", err)
		}
		for _, it := range out.Items {
			ops = append(ops, types.TransactWriteItem{Delete: &types.Delete{
				TableName: aws.String(st.Table), Key: item{"pk": it["pk"], "sk": it["sk"]}}})
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	now := ts(st.now())
	for _, c := range codes {
		it := recoveryKey(userID, codeHash(userID, normaliseCode(c)))
		it["createdAt"] = s(now)
		ops = append(ops, types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: it}})
	}
	if len(ops) == 0 {
		return nil
	}
	return dynamo.TransactWrite(ctx, st.DB, ops...)
}

// UseRecoveryCode deletes the code's item and reports whether it was there, so
// of two requests with one code only one wins.
func (st *Store) UseRecoveryCode(ctx context.Context, userID, normalised string) (bool, error) {
	_, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.Table), Key: recoveryKey(userID, codeHash(userID, normalised)),
		ConditionExpression: aws.String(exists),
	})
	if dynamo.IsConditionFailed(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("use recovery code: %w", err)
	}
	return true, nil
}

// CountRecoveryCodes is how many codes the user has left.
func (st *Store) CountRecoveryCodes(ctx context.Context, userID string) (int, error) {
	total := 0
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true), Select: types.SelectCount,
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :r)"),
			ExpressionAttributeValues: item{":p": s("USER#" + userID), ":r": s("RECOVERY#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return 0, fmt.Errorf("count recovery codes: %w", err)
		}
		total += int(out.Count)
		if len(out.LastEvaluatedKey) == 0 {
			return total, nil
		}
		start = out.LastEvaluatedKey
	}
}

// FactorReset is a confirmed request to purge the user's factors once DueAt has passed.
type FactorReset struct{ RequestedAt, DueAt time.Time }

func factorResetKey(userID string) item {
	return item{"pk": s("USER#" + userID), "sk": s("FACTORRESET")}
}

// GetFactorReset returns the pending reset, or nil when there is none.
func (st *Store) GetFactorReset(ctx context.Context, userID string) (*FactorReset, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: factorResetKey(userID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("get factor reset: %w", err)
	}
	if out.Item == nil {
		return nil, nil
	}
	req, err := getT(out.Item, "requestedAt")
	if err != nil {
		return nil, err
	}
	due, err := getT(out.Item, "dueAt")
	if err != nil || due == nil {
		return nil, fmt.Errorf("factor reset dueAt: %v", err)
	}
	fr := &FactorReset{DueAt: *due}
	if req != nil {
		fr.RequestedAt = *req
	}
	return fr, nil
}

// DeleteFactorReset cancels a pending reset; none pending is not an error.
func (st *Store) DeleteFactorReset(ctx context.Context, userID string) error {
	if _, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(st.Table), Key: factorResetKey(userID)}); err != nil {
		return fmt.Errorf("delete factor reset: %w", err)
	}
	return nil
}

// issueRecoveryCodes replaces the user's set with ten new codes and returns them.
func (a *App) issueRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := a.Store.ReplaceRecoveryCodes(ctx, userID, codes); err != nil {
		return nil, err
	}
	return codes, nil
}

// regenerateRecoveryCodes gives a full session a new set and voids the old one.
func (a *App) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "regenerate-recovery-codes", "")
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	codes, err := a.issueRecoveryCodes(r.Context(), u.ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
	return nil
}

// recovery trades the password's half session and one recovery code for an
// enroll session that counts as having proved a factor: the person then adds a
// new factor, which settles the session to full. A wrong code is a failed
// sign-in: it counts for the lockout and the windowed counters, and a lock ends
// the session.
func (a *App) recovery(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	x, u := sessionFrom(ctx)
	var in struct{ Code string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	now := a.now()
	if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
		return a.refuseRecovery(w, r, x)
	}
	ok, err := a.Store.UseRecoveryCode(ctx, u.ID, normaliseCode(in.Code))
	if err != nil {
		return err
	}
	ip, _ := a.clientIP(r)
	if !ok {
		setAuditDetail(ctx, "reason", "wrong_code")
		if err := a.failSignIn(ctx, u, ip, emailHash(u.Email)); err != nil {
			return err
		}
		if cur, err := a.Store.GetUser(ctx, u.ID); err == nil && cur.LockedUntil != nil && now.Before(*cur.LockedUntil) {
			return a.refuseRecovery(w, r, x)
		}
		return errInvalidCredentials
	}
	if err := a.completeSignIn(ctx, u); err != nil {
		return err
	}
	x.FactorProved = true
	if _, err := a.reissue(ctx, w, x, StageEnroll, x.Epoch); err != nil {
		return err
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "recovery-code-used"})
	left, err := a.Store.CountRecoveryCodes(ctx, u.ID)
	if err != nil {
		slog.Warn("portal: count recovery codes", "err", err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"next": "enroll", "recoveryCodesLeft": left})
	return nil
}

// refuseRecovery ends the half session of a locked account and answers like any
// other failed sign-in.
func (a *App) refuseRecovery(w http.ResponseWriter, r *http.Request, x Session) error {
	if err := a.endSession(w, r, x); !errors.Is(err, errUnauthorized) {
		return err
	}
	return errInvalidCredentials
}
