package portal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

const (
	maxEmailBytes   = 254
	maxDisplayName  = 64
	verifyTokenLife = 24 * time.Hour
	sideEffectLimit = 10 * time.Second
	// answerFloor is the least time a 202 on sign-up and resend takes, so the
	// work only an existing account causes (token write, send) cannot be timed.
	answerFloor = 1500 * time.Millisecond
)

var (
	// signupIPLimit: 5 at once, then one per 10 minutes.
	signupIPLimit = ratelimit.Settings{Burst: 5, PerSecond: 1.0 / 600}
	// resendIPLimit: 10 per hour.
	resendIPLimit = ratelimit.Settings{Burst: 10, PerSecond: 1.0 / 360}
	// emailLimit: 3 emails per address per hour.
	emailLimit = ratelimit.Settings{Burst: 3, PerSecond: 1.0 / 1200}
)

func apiErr(status int, code string) *webapi.Error { return &webapi.Error{Status: status, Code: code} }

func checkEmail() map[string]string { return map[string]string{"status": "check_email"} }

// emailHash is the key a limiter holds for an address: never the address itself.
func emailHash(normalised string) string {
	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:])
}

// parseEmail accepts one bare address of at most 254 bytes, no display name,
// and returns it normalised.
func parseEmail(raw string) (string, bool) {
	e := NormalizeEmail(raw)
	if e == "" || len(e) > maxEmailBytes {
		return "", false
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Name != "" || a.Address != e {
		return "", false
	}
	return e, true
}

func validName(raw string) (string, bool) {
	n := strings.TrimSpace(raw)
	if c := utf8.RuneCountInString(n); c < 1 || c > maxDisplayName || hasURL(n) {
		return "", false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return n, true
}

func validLang(l string) bool { return l == "en" || l == "vi" }

// turnstileOK checks the widget token for the caller. A missing or refused
// token is 403 turnstile_failed; Cloudflare being unreachable is 503.
func (a *App) turnstileOK(r *http.Request, token string) error {
	if strings.TrimSpace(token) == "" {
		return apiErr(http.StatusForbidden, "turnstile_failed")
	}
	ip, _ := a.clientIP(r)
	ok, err := a.Turnstile.Verify(r.Context(), token, ip)
	if err != nil {
		slog.Warn("portal: turnstile unavailable", "err", err)
		return apiErr(http.StatusServiceUnavailable, "turnstile_unavailable")
	}
	if !ok {
		return apiErr(http.StatusForbidden, "turnstile_failed")
	}
	return nil
}

// limitEmail spends one of the address's 3 emails per hour. The same bucket
// serves a new and a known address, so the limit says nothing about either.
func (a *App) limitEmail(ctx context.Context, normalised string) error {
	if d := a.Limiter.Allow(ctx, "email#"+emailHash(normalised), emailLimit); !d.Allowed {
		slog.Warn("portal: email rate limited")
		return rateLimited(d.RetryAfterSec)
	}
	return nil
}

// sendVerify stores a fresh verify token for the user and mails its link. The
// link carries the token in the fragment, so it never reaches CloudFront or a
// server log. The token holds the account's epoch and address, so a later
// sign-up that replaces the pending account voids it.
func (a *App) sendVerify(ctx context.Context, u User) error {
	token, hash, err := a.newToken()
	if err != nil {
		return err
	}
	if err := a.Store.PutToken(ctx, Token{Hash: hash, Purpose: PurposeVerify, UserID: u.ID, Email: NormalizeEmail(u.Email),
		ExpiresAt: a.now().Add(verifyTokenLife), Epoch: u.SessionEpoch}); err != nil {
		return err
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "verify",
		Vars: map[string]string{"link": a.Config.PublicBaseURL + "/verify-email#token=" + token}})
	return nil
}

// mailBestEffort sends, and logs a failure instead of answering it: a failed
// send for one kind of address and not the other would tell them apart. The log
// line has the kind and the error text with every address in it redacted (SES
// names the recipient in some errors), never the recipient.
func (a *App) mailBestEffort(ctx context.Context, m pmail.Message) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectLimit)
	defer cancel()
	if err := a.Mail.Send(ctx, m); err != nil {
		slog.Error("portal: email send failed", "kind", m.Kind, "err", redactEmails(err.Error()))
	}
}

// emailLike is anything shaped like an address: a run of non-space characters
// around an @. It is wider than a valid address on purpose.
var emailLike = regexp.MustCompile(`[^\s<>"'(),;:\[\]]*@[^\s<>"'(),;:\[\]]*`)

// redactEmails replaces every address-like run in s.
func redactEmails(s string) string { return emailLike.ReplaceAllString(s, "<redacted>") }

type signupReq struct {
	Email, DisplayName, Password, Lang, TurnstileToken string
}

// signup answers 202 check_email for a new and for a known address alike. An
// address held by a pending (unverified) account is taken over by the newest
// sign-up: whoever can read the mailbox and knows the password typed last is
// the one who verifies it, so a stranger who signed up first with someone
// else's address cannot keep it. A verified holder is mailed signup-exists.
func (a *App) signup(w http.ResponseWriter, r *http.Request) error {
	var in signupReq
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	email, ok := parseEmail(in.Email)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_email")
	}
	name, ok := validName(in.DisplayName)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_name")
	}
	if password.Validate(in.Password) != nil {
		return apiErr(http.StatusBadRequest, "invalid_password")
	}
	if !validLang(in.Lang) {
		return apiErr(http.StatusBadRequest, "invalid_lang")
	}
	if err := a.spendIP(r, "signup-ip", signupIPLimit); err != nil {
		return err
	}
	if err := a.turnstileOK(r, in.TurnstileToken); err != nil {
		return err
	}
	switch breached, err := a.Passwords.Breached(r.Context(), in.Password); {
	case err != nil:
		slog.Warn("portal: breached-password check failed; letting the password through", "err", err)
	case breached:
		return apiErr(http.StatusBadRequest, "password_breached")
	}
	if err := a.limitEmail(r.Context(), email); err != nil {
		return err
	}
	// Hash before deciding, so a known address costs the same as a new one.
	hash, err := password.Hash(in.Password)
	if err != nil {
		return err
	}
	u := User{ID: id.New(), Email: email, DisplayName: name, Lang: in.Lang, PasswordHash: hash, CreatedAt: a.now()}
	switch err := a.Store.CreateUser(r.Context(), u); {
	case err == nil:
		if err := a.sendVerify(r.Context(), u); err != nil {
			slog.Error("portal: sign-up verify email not issued", "err", err)
		}
	case errors.Is(err, ErrEmailTaken):
		a.signupTaken(r.Context(), u)
	default:
		slog.Error("portal: sign-up account write failed", "err", err)
	}
	httpx.WriteJSON(w, http.StatusAccepted, checkEmail())
	return nil
}

// signupTaken handles a sign-up u for an address that already has an account.
// A pending holder is replaced by u and mailed a fresh link; a verified one is
// told someone tried. Failures are logged: the answer is 202 either way.
func (a *App) signupTaken(ctx context.Context, u User) {
	known, err := a.Store.GetUserByEmail(ctx, u.Email)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return // lapsed or deleted between the two calls; the next sign-up creates it
	case err != nil:
		slog.Error("portal: sign-up lookup failed", "err", err)
		return
	case known.EmailVerifiedAt == nil:
		cur, err := a.Store.ReplacePending(ctx, known, u, a.now())
		if err != nil {
			slog.Error("portal: sign-up over a pending account failed", "err", err)
			return
		}
		if err := a.sendVerify(ctx, cur); err != nil {
			slog.Error("portal: sign-up verify email not issued", "err", err)
		}
		return
	}
	lang := u.Lang
	if validLang(known.Lang) {
		lang = known.Lang // the owner's language, not the stranger's
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: lang, Kind: "signup-exists"})
}

// verifyEmail checks the link and the account's password, then spends the
// link, creates the personal workspace and opens an enroll session. A bad link
// and a wrong password are the same 400 invalid_token. A wrong password counts
// like a failed sign-in and leaves the link unspent, so the owner can retype;
// a locked account is refused whatever the password.
func (a *App) verifyEmail(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Token, Password string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	invalid := apiErr(http.StatusBadRequest, "invalid_token")
	if in.Token == "" || len(in.Token) > 128 {
		return invalid
	}
	hash := hashToken(in.Token)
	t, err := a.Store.PeekToken(ctx, PurposeVerify, hash, a.now())
	if errors.Is(err, ErrTokenInvalid) {
		return invalid
	}
	if err != nil {
		return err
	}
	u, err := a.Store.GetUser(ctx, t.UserID)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return invalid
	case err != nil:
		return err
	case u.EmailVerifiedAt != nil || u.SessionEpoch != t.Epoch || NormalizeEmail(u.Email) != t.Email:
		return invalid
	}
	ip, _ := a.clientIP(r)
	acct := emailHash(NormalizeEmail(u.Email))
	if u.LockedUntil != nil && a.now().Before(*u.LockedUntil) {
		setAuditDetail(ctx, "reason", "locked")
		if err := a.countFailure(ctx, ip, acct); err != nil {
			return err
		}
		return invalid
	}
	if !a.verifyPassword(in.Password, u.PasswordHash) {
		setAuditDetail(ctx, "reason", "wrong_password")
		if err := a.failSignIn(ctx, u, ip, acct); err != nil {
			return err
		}
		return invalid
	}
	if _, err := a.Store.UseToken(ctx, PurposeVerify, hash, a.now()); errors.Is(err, ErrTokenInvalid) {
		return invalid
	} else if err != nil {
		return err
	}
	tenant := Tenant{ID: id.New()}
	switch err := a.Store.VerifyPending(ctx, u, tenant, a.now()); {
	case errors.Is(err, ErrUserNotFound), errors.Is(err, ErrAlreadyVerified), errors.Is(err, ErrConflict):
		return invalid
	case err != nil:
		return err
	}
	u, err = a.Store.GetUser(ctx, t.UserID)
	if err != nil {
		return err
	}
	setAuditActor(r.Context(), u.ID)
	setAuditTenant(r.Context(), tenant.ID)
	if err := a.startSession(r.Context(), w, u, StageEnroll); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"next": "enroll"})
	return nil
}

// resendVerification mails a fresh link only for an existing unverified
// account; the answer is the same either way.
func (a *App) resendVerification(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Email, TurnstileToken string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	email, ok := parseEmail(in.Email)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_email")
	}
	if err := a.spendIP(r, "resend-ip", resendIPLimit); err != nil {
		return err
	}
	if err := a.turnstileOK(r, in.TurnstileToken); err != nil {
		return err
	}
	if err := a.limitEmail(r.Context(), email); err != nil {
		return err
	}
	switch u, err := a.Store.GetUserByEmail(r.Context(), email); {
	case err == nil && u.EmailVerifiedAt == nil:
		if err := a.sendVerify(r.Context(), u); err != nil {
			slog.Error("portal: resend verify email not issued", "err", err)
		}
	case err == nil, errors.Is(err, ErrUserNotFound):
	default:
		slog.Error("portal: resend lookup failed", "err", err)
	}
	httpx.WriteJSON(w, http.StatusAccepted, checkEmail())
	return nil
}

// config is the public configuration the sign-up page needs.
func (a *App) config(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"turnstileSiteKey": a.Config.TurnstileSiteKey})
}

func (a *App) mountSignup(mux *http.ServeMux) {
	mux.Handle("POST /api/auth/signup", a.audited("signup", webapi.HandlerFunc(a.evenly(answerFloor, a.signup))))
	mux.Handle("POST /api/auth/verify-email", a.audited("email-verified", webapi.HandlerFunc(a.verifyEmail)))
	mux.Handle("POST /api/auth/resend-verification", a.audited("resend-verification", webapi.HandlerFunc(a.evenly(answerFloor, a.resendVerification))))
	mux.HandleFunc("GET /api/config", a.config)
}

// bufferedResponse holds an answer until evenly decides to release it.
type bufferedResponse struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.h }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

// evenly runs h into a buffer and releases a success only once floor has passed
// since the request started. An error answers at once: the ones these routes
// return (malformed input, Turnstile, limits) do not depend on whether the
// address has an account.
func (a *App) evenly(floor time.Duration, h func(http.ResponseWriter, *http.Request) error) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		start := a.now()
		buf := &bufferedResponse{h: http.Header{}, status: http.StatusOK}
		if err := h(buf, r); err != nil {
			return err
		}
		if wait := floor - a.now().Sub(start); wait > 0 {
			a.sleep(r.Context(), wait)
		}
		for k, v := range buf.h {
			w.Header()[k] = v
		}
		w.WriteHeader(buf.status)
		_, _ = w.Write(buf.body.Bytes())
		return nil
	}
}

func (a *App) sleep(ctx context.Context, d time.Duration) {
	if a.Sleep != nil {
		a.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
