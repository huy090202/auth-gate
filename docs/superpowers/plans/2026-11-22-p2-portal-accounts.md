# P2: the Portal, accounts and the personal workspace — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A stranger opens the Portal, signs up, verifies the email, enrols a PIN or passkey, keeps recovery codes, creates a client in their personal workspace, gets its key and calls AuthGate with it; they can recover a forgotten password or a lost factor; the Portal runs on stg and in CI.

**Architecture:** A third app in the Console's image: `cmd/portal` (Go, one Lambda behind CloudFront), `internal/portal`, `portal-web/` (React + Mantine), `infra/portal.yaml`, its own table. It reaches AuthGate only over HTTP: its own client key for its users' PIN and passkeys (subjects are user ids), and the portal key for `/api/portal/*` with the workspace's tenant id. Code both apps need moves into shared packages first (Go under `internal/platform`, web under `packages/web-kit`), so the Portal copies nothing from the Console.

**Tech Stack:** Go 1.26, DynamoDB, SES v2 (aws-sdk-go-v2), S3 presigned POST, Cloudflare Turnstile, Have I Been Pwned range API, React 19 + Mantine 7 + Vite 6 + vitest, SAM, Bruno, bash.

**Spec:** `docs/superpowers/specs/2026-11-22-self-service-design.md` — sections 3, 5, 6 (personal workspace only), 7, 9, and the P2 row of section 11. Organisations (section 8) are P3; the operator side (section 10) is P4.

## Global Constraints

- The Portal never holds the admin key. `cmd/portal` imports neither `internal/auth/service` nor `internal/console` (enforced in `make check`, like the Console rule).
- AuthGate calls: verify routes with the Portal's own client key (`/credo/auth-gate-portal/<stage>/authgate-client-key`); `/api/portal/*` with the portal key (`/credo/auth-gate/<stage>/portal-key`) and `x-tenant-id` = the workspace's tenant id. Subjects are Portal user ids.
- SSM under `/credo/auth-gate-portal/<stage>/`: `authgate-client-key` (SecureString), `origin-verify` (String), `turnstile-secret` (SecureString). Never on argv, never printed.
- Table `auth-gate-portal-<stage>`, keys `pk`/`sk`, TTL attribute `ttl`, deletion protection on, item families exactly as spec section 5; timestamps in the Console's `tsLayout`.
- Ids: user and tenant ids are `id.New()` ULIDs. Emails are normalised (trim, lower-case the whole address) before any key or comparison; a limiter or index key never holds a raw email (`sha256` hex of the normalised email).
- Passwords: 12–128 bytes, argon2id with the Console's parameters, plus the HIBP range check (first 5 hex of SHA-1 only; 2 s timeout; fail open with a log line).
- Tokens sent by email: 32 random bytes, base64url in the link, only SHA-256 stored, single use. Lifetimes: verify 24 h; password reset 1 h; factor-reset link 1 h then a 24 h wait; email change 24 h. Unverified accounts expire after 7 days (TTL on the user and email items).
- Recovery codes: 10, each 10 characters from Crockford base32 shown as `XXXXX-XXXXX`, SHA-256 stored, single use, shown once.
- Sessions: the Console's model (server-side item, `__Host-portal_session` cookie, `SameSite=Strict`, stages `half`/`enroll`/`full`, `sessionEpoch`, `FactorProved`, the 25 s first-factor lease, TTLs half 10 m, enroll 30 m, full 30 m idle / 8 h absolute). Login purpose `portal-login`; step-up purposes `portal-<action>` bound to the target as in the Console.
- Step-up guards: `rotate-key`, `revoke-previous-keys`, `disable-client`, `purge-subject`, `delete-client`, `change-password`, `change-email`, `add-factor`, `remove-factor`, `regenerate-recovery-codes`, `delete-account`.
- Enumeration: sign-up, resend, forgotten password and factor-reset request always answer `202 {"status":"check_email"}`; login failures always answer `401 invalid_credentials` (an unverified email is revealed only after the right password: `403 email_unverified`).
- Turnstile: always on sign-up, forgotten password, resend, factor-reset request (Cloudflare failure → `503 turnstile_unavailable`); on login after 2 failures in 15 minutes for the IP or the account (`403 turnstile_required` when missing or wrong). Local and tests use Cloudflare's test keys (site `1x00000000000000000000AA`, secret `1x0000000000000000000000000000000AA`).
- Rate limits (token bucket in the Portal table, `429 rate_limited` + `Retry-After`): sign-up per IP burst 5 then 1 per 600 s, emails per address 3 per hour (burst 3, 1 per 1200 s); login per IP burst 10 then 1 per 6 s, 5 failures lock the account 15 minutes; forgotten password and resend per IP 10 per hour (burst 10, 1 per 360 s) and per address 3 per hour; any signed-in call per user burst 60 then 1 per second.
- Emails: SES v2 `SendEmail` from `EMAIL_FROM`; templates are fixed text in EN and VI by the user's `lang` (chosen at sign-up, `en` or `vi`). In P2 nothing a user typed is interpolated, not even the display name: a `signup-exists` email would otherwise carry a stranger's text to the address owner. Only `link` and `until` are filled in. Every email says what to do if it was not you. Local and tests write to a mailbox directory (`MAILBOX_DIR`).
- Avatars: PNG, JPEG or WebP, at most 1 MiB, uploaded by presigned S3 POST (content-length-range and content-type conditions) to `avatars/<userId>/<ulid>.<ext>` in bucket `auth-gate-portal-<stage>-avatars-<account>`, served at `/avatars/*`; confirmed by the API (HeadObject size and type) before it is set.
- The personal workspace: created at email verification, `kind=personal`, the user its only member with role `owner`, never renamed, deleted only with the account.
- Fixed monthly cost: none. No WAF in P2.
- No step calls AWS. Never touch `~/.aws`. English in code, comments, docs and commits; UI strings EN and VI. Conventional Commits ending `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `pnpm run check` and `make test-store` pass at the end of every task.

## Review Focus

- An email that differs only by case or surrounding spaces (`A@x.com `) must hit the same account on sign-up, login, reset and verification. Test in Task 5.
- A verify, reset or factor-reset link used twice, or after its lifetime, must do nothing the second time and answer the same as an unknown token. Tests in Tasks 5 and 7.
- A session whose user was suspended, deleted, or whose epoch moved (password reset in another tab) must be refused on its next request, including on `/api/t/...` routes. Test in Task 6.
- A user calling `/api/t/<another user's personal tenant>/clients` must get `404`, the same as an unknown tenant, with no AuthGate call. Test in Task 9.
- A recovery code typed in lower case or without the dash must still work, once. Test in Task 6.

---

### Task 1: Shared Go packages for both web apps

**Files:**
- Move: `internal/console/authgate/` → `internal/platform/authgate/` (package `authgate`; Console updated).
- Create: `internal/platform/password/` (`Hash`, `Verify`, `Validate`, `DummyHash` from `internal/console/password.go`), `internal/platform/viewerip/` (`FromHeader(h http.Header) string` from `internal/console/ip.go`), `internal/platform/webapi/` (`Error` (was `apiError`), `HandlerFunc`, `WriteErr`, `RequireJSON`, `NoStore`, `DecodeBody`, `WriteRaw`, `AuthGateError` (was `agError`)).
- Modify: `internal/console/*` to use them; `Makefile` `check` (add the same dependency rule for `./cmd/portal` against `internal/auth/service` and `internal/console`, guarded so it passes before `cmd/portal` exists).
- Test: move the existing tests with the code (`password_test.go`, `ip_test.go`, the authgate tests).

**Interfaces:**
- Produces: the packages above with the Console's exact behaviour; plus in `authgate`: `type Tenant struct{ ... }` returned by `(c *Client) Tenant(id string) *TenantClient`, whose methods call `/api/portal/clients…` with the portal key in `Client.PortalKey` and header `x-tenant-id`: `List(ctx, cursor) (json.RawMessage, error)`, `Get`, `Create(ctx, body any)`, `Update(ctx, id, body any)`, `Disable`, `Enable`, `RotateKey`, `RevokePreviousKeys`, `PurgeSubject(ctx, id, subject) error`, `Delete(ctx, id) error`. `rolePath` keeps mapping `/api/portal` to `/admin` locally.

- [ ] **Step 1:** Move the code and its tests; `go build ./... && make test-store` green with no test assertion changed except import paths and renamed identifiers.
- [ ] **Step 2: Write the failing test** `TestTenantClientSendsPortalKeyAndTenant` (httptest server): every `TenantClient` method sends `x-internal-access-key: <PortalKey>` and `x-tenant-id: <id>` to the right method and path, never the admin or client key; a 404 `client_not_found` comes back as `*authgate.Error{Status:404, Code:"client_not_found"}`.
- [ ] **Step 3:** Implement `TenantClient`; tests pass.
- [ ] **Step 4: Gate and commit** — `pnpm run check`, `make test-store`; `git commit -m "refactor: shared authgate client, password, viewer ip and web api packages for the console and the portal"`.

### Task 2: Shared web kit

**Files:**
- Create: `packages/web-kit/` (name `@authgate/web-kit`, private, type module, `exports` of TS source), containing from console-web: `ui.tsx`, `controls.tsx`, `toast.tsx`, `ShownOnce.tsx`, `theme.ts`, `lib/webauthn.ts`, `lib/format.ts`, `lib/mantine-vars.ts`, and generic `lib/api.ts` and `lib/i18n.ts` (see Interfaces); their tests and fixtures move with them.
- Modify: `pnpm-workspace.yaml` (`packages/*`, `console-web`, later `portal-web`), `console-web` imports the kit, keeps its own strings, routes, session, stepup, brand and screens.

**Interfaces:**
- `createApi<Code extends string>(codes: readonly Code[])` returns `{ api, ApiError, onApiFailure }` with the current behaviour; `createI18n<S>(storageKey: string, strings: { en: S; vi: S })` returns `{ useLang, useStrings, Lang }`. The kit's own components take their strings as props (no app string table inside the kit).

- [ ] **Step 1:** Move; `pnpm --filter console-web run typecheck && pnpm -r test` green with Console behaviour unchanged (the CSP test, the session test and the strings test still pass unmodified apart from imports).
- [ ] **Step 2: Gate and commit** — `pnpm run check`; `git commit -m "refactor: a shared web kit for the console and the portal"`.

### Task 3: Portal foundation: config, store, sessions, binary

**Files:**
- Create: `cmd/portal/main.go`, `internal/portal/{config.go,app.go,store.go,session.go,audit.go,limit.go}` and tests; `Makefile` targets `portal-dev`, `portal-build` (copy `console-dev`/`console-build`).

**Interfaces:**
- `Config`: env `STAGE`, `TABLE_NAME`, `AWS_REGION`, `AUTHGATE_BASE_URL`, `AUTHGATE_CLIENT_ID`, `PUBLIC_BASE_URL`, `EMAIL_FROM`, `AVATAR_BUCKET`, `TURNSTILE_SITE_KEY`, `CLIENT_KEY_PARAM`, `PORTAL_KEY_PARAM`, `TURNSTILE_SECRET_PARAM`, `ORIGIN_VERIFY_PARAM`, `DYNAMO_ENDPOINT`, `PORT` (default 8789), `PORTAL_DEV=1` (dev: `CLIENT_KEY`, `PORTAL_KEY` default `dev-portal`, `MAILBOX_DIR`, Turnstile test keys, avatar uploads to a local directory served by the dev server).
- `Deps{Config; DB; Limiter; AuthGate *authgate.Client; Tokens *authgate.Verifier; Mail mail.Sender; Turnstile turnstile.Verifier; Passwords hibp.Checker; Avatars avatar.Store; Now func() time.Time}`; `New(d Deps) http.Handler` with the Console's wrapping (`webapi.NoStore(webapi.RequireJSON(mux))`, `/api/` → 404 `not_found`).
- `Store` methods for every item in spec section 5 used in P2: `CreateUser(ctx, User) error` (user + `EMAIL#` in one transaction, `ErrEmailTaken`), `GetUserByEmail`, `GetUser`, `VerifyEmail(ctx, userID string, tenant Tenant, now) error` (one transaction: clears the user's and email item's `ttl`, sets `emailVerifiedAt`, writes the tenant `META`, `TENANTS` index and the membership pair), `PutToken(ctx, Token) error`, `UseToken(ctx, purpose, hash string, now) (Token, error)` (conditional delete of an unexpired token of that purpose; `ErrTokenInvalid` for absent, expired or wrong purpose), session methods as the Console's (`PutSession`, `ReplaceSession`, `GetSession`, `DeleteSession`, `UseJTI`, lease, lockout, epoch), `ListMemberships(ctx, userID)`, `GetMembership(ctx, tenantID, userID)`, `PutAudit`/`ListAudit` keyed `TENANT#<id>` / `AUDIT#<time>#<id>` with `ttl` 180 days.
- `a.limitUser` spends the per-user signed-in bucket on every `StageFull` request (`429 rate_limited`); `a.limitIP(scope, settings)` for the public auth routes.
- `a.session(stages...)` as the Console's; `a.member(role ...string)` middleware for `/api/t/{tenantId}/…`: reads the membership with a consistent read on every request; absent or wrong role → `404 not_found` for a tenant the user is not in, `403 forbidden` for a role too low.

- [ ] **Step 1: Write the failing store tests** (DynamoDB Local): `TestCreateUserRejectsDuplicateEmail`; `TestVerifyEmailCreatesPersonalWorkspace` (one transaction: user and email lose `ttl`, tenant `kind=personal`, membership pair with `owner`, `TENANTS` item); `TestUnverifiedUserCarriesSevenDayTTL`; `TestUseTokenIsSingleUseAndExpires` (second use and use after expiry → `ErrTokenInvalid`; wrong purpose → `ErrTokenInvalid`); `TestSessionEpochMismatchIsRefused` (via `a.session`).
- [ ] **Step 2:** Run, see them fail; implement; pass.
- [ ] **Step 3:** `go run ./cmd/portal` in dev mode answers `GET /api/nope` with `404 {"error":"not_found"}`.
- [ ] **Step 4: Gate and commit** — `git commit -m "feat(portal): binary, config, store and sessions"`.

### Task 4: Email, Turnstile, Have I Been Pwned, avatars storage

**Files:**
- Create: `internal/portal/mail/` (`Sender` interface `Send(ctx, Message) error`; `Message{To, Lang, Kind, Vars map[string]string}`; `SES` using sesv2 `SendEmail` with `EMAIL_FROM`; `Dir` writing one JSON file per message to `MAILBOX_DIR`; `Render(Message) (subject, text string, err error)` from fixed EN/VI templates), `internal/portal/turnstile/` (`Verifier` interface `Verify(ctx, token, ip string) (bool, error)`; Cloudflare `siteverify` client with a 5 s timeout), `internal/portal/hibp/` (`Checker` interface `Breached(ctx, password string) (bool, error)`; range API client with a 2 s timeout and `Add-Padding: true`), `internal/portal/avatar/` (`Store` interface `PresignPost(ctx, key, contentType string) (PostForm, error)`, `Head(ctx, key) (size int64, contentType string, err error)`, `Delete(ctx, key) error`; S3 implementation; local directory implementation).

**Interfaces:**
- Email kinds: `verify`, `signup-exists`, `reset`, `factor-reset-confirm`, `factor-reset-started`, `email-change-confirm`, `email-changed`, `password-changed`, `factor-added`, `factor-removed`, `recovery-code-used`, `account-locked`, `account-deleted`. `Vars` keys: `link`, `until` (no user-supplied text).

- [ ] **Step 1: Write the failing tests**: `TestRenderEveryKindInBothLanguages` (every kind renders in `en` and `vi`, contains the "if this was not you" line, contains `link` when given); `TestRenderRefusesUnknownVars` (a `Vars` key other than `link` or `until` → error); `TestDirSenderWritesOneFilePerMessage`; `TestTurnstileClientPostsSecretAndToken` (httptest; secret only in the POST body); `TestHIBPSendsOnlyThePrefix` (httptest asserts the path is the 5-hex prefix and no other part of the hash is sent; a suffix match → breached; a 2 s timeout → error); `TestAvatarPresignConditions` (the S3 policy carries `content-length-range 1..1048576` and the exact content type).
- [ ] **Step 2:** Implement; tests pass.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(portal): email, turnstile, breached-password check and avatar storage"`.

### Task 5: Sign-up and email verification

**Files:** `internal/portal/signup.go` and test.

**Interfaces:**
- `POST /api/auth/signup` `{email, displayName, password, lang, turnstileToken}` → always `202 {"status":"check_email"}` unless the input is malformed (`400 invalid_email|invalid_name|invalid_password|password_breached`), Turnstile failed (`403 turnstile_failed`; `503 turnstile_unavailable`), or a limit hit (`429`). New email: user created unverified, `verify` email. Known email: `signup-exists` email, nothing written.
- `POST /api/auth/verify-email` `{token}` → `200 {"next":"enroll"}` and an `enroll` session; bad, used or expired token → `400 invalid_token`.
- `POST /api/auth/resend-verification` `{email, turnstileToken}` → always `202`; sends a fresh `verify` email only for an existing unverified account.
- `GET /api/config` → `{turnstileSiteKey}` (public, no session).

- [ ] **Step 1: Write the failing tests**: `TestSignupAnswersSameForNewAndKnownEmail` (status and body identical; mailbox holds `verify` for the new one and `signup-exists` for the known one); `TestSignupNormalisesEmail` (`" A@X.com "` then `"a@x.com"` → the second gets `signup-exists`); `TestSignupRefusesBreachedPassword` (fake checker says breached → `400 password_breached`); `TestSignupFailsOpenWhenHIBPDown` (checker errors → `202`, a log line); `TestSignupRequiresTurnstile` (missing or failed token → `403 turnstile_failed`, nothing written; verifier error → `503`); `TestSignupRateLimits` (6th sign-up from one IP inside the window → `429`); `TestVerifyEmailCreatesWorkspaceAndEnrollSession`; `TestVerifyTokenSingleUse` (second use → `400 invalid_token`, no second tenant).
- [ ] **Step 2:** Implement; pass. **Step 3: Gate and commit** — `git commit -m "feat(portal): sign-up and email verification, without revealing who has an account"`.

### Task 6: Sign-in, factors, recovery codes and step-up

**Files:** `internal/portal/{auth.go,me.go,recovery.go,stepup.go}` and tests.

**Interfaces:**
- `POST /api/auth/login` `{email, password, turnstileToken?}` → `{"next":"second-factor","factors":{pin,webauthn}}` (stage `half`), `{"next":"enroll"}` (no factor yet), `401 invalid_credentials` (unknown, wrong, locked, suspended), `403 email_unverified` (right password, unverified), `403 turnstile_required`. A factor reset past its due time is applied here (factors purged, `FACTORRESET` removed, stage `enroll`).
- `POST /api/auth/second-factor/start|verify` as the Console's, purpose `portal-login`; a successful verify also cancels a pending factor reset (and sends nothing).
- `POST /api/auth/recovery` `{code}` (stage `half`) → consumes the code, `enroll` session with `FactorProved`, `recovery-code-used` email.
- `POST /api/auth/logout`; `GET /api/me` (`{userId, email, displayName, lang, avatarUrl?, stage, factors, recoveryCodesLeft, personalTenantId}`).
- `POST /api/me/pin`, `DELETE /api/me/pin`, `POST /api/me/passkeys/start|finish`, `DELETE /api/me/passkeys/{id}` as the Console's (first factor under the lease; later ones need step-up `add-factor`/`remove-factor`; the last factor cannot be removed: `409 last_factor`). The response that adds the first factor carries `recoveryCodes: [10 codes]`, shown once.
- `POST /api/me/recovery-codes` (step-up `regenerate-recovery-codes`) → `{recoveryCodes}`; the old set is deleted in the same transaction.
- `POST /api/stepup/start|verify` as the Console's with the actions in Global Constraints.
- `normaliseCode(s string) string`: upper-case, drop spaces and dashes, then re-insert the dash.

- [ ] **Step 1: Write the failing tests**: `TestLoginFailuresLookAlike` (unknown, wrong password, suspended → same `401` body); `TestLoginUnverifiedOnlyAfterRightPassword`; `TestLoginLocksAfterFiveFailures`; `TestLoginNeedsTurnstileAfterTwoFailures` (per IP and per account); `TestFirstFactorIssuesTenRecoveryCodesOnce`; `TestRecoveryCodeNormalisedAndSingleUse` (`abcde-fghij`, `ABCDEFGHIJ`, `abcde fghij` all match; second use → `401 invalid_credentials`); `TestRecoveryCodeLandsInEnroll`; `TestSessionRefusedAfterEpochBump`; `TestLastFactorCannotBeRemoved`; `TestStepUpBoundToTarget` (a token for `rotate-key` on client A refused for client B).
- [ ] **Step 2:** Implement (reuse the Console's flow structure and tests as the model); pass. **Step 3: Gate and commit** — `git commit -m "feat(portal): sign-in with a mandatory second factor, recovery codes and step-up"`.

### Task 7: Forgotten password and lost factor

**Files:** `internal/portal/reset.go` and test.

**Interfaces:**
- `POST /api/auth/forgot` `{email, turnstileToken}` → always `202`; a `reset` email for a verified account.
- `POST /api/auth/reset-password` `{token, newPassword}` → `204`; sets the password, bumps `sessionEpoch`, resets lockout, `password-changed` email; bad token → `400 invalid_token`.
- `POST /api/auth/factor-reset/request` `{email, turnstileToken}` → always `202`; a `factor-reset-confirm` email for a verified account.
- `POST /api/auth/factor-reset/confirm` `{token}` → `200 {"dueAt"}`; writes `FACTORRESET` (`dueAt = now + 24h`) and sends `factor-reset-started` with `until`. The purge happens at the next login after `dueAt` (Task 6); a verified second factor before then cancels it.

- [ ] **Step 1: Write the failing tests**: `TestForgotAnswersSameForUnknown`; `TestResetPasswordEndsSessionsAndKeepsFactor` (an old session is refused; the next login still asks for the second factor); `TestResetTokenExpiresAfterAnHour` (injected clock); `TestFactorResetWaitsTwentyFourHours` (login at +23 h still asks for the old factor; at +24 h it lands in `enroll` with no factors and AuthGate's factors purged); `TestFactorResetCancelledBySecondFactor`.
- [ ] **Step 2:** Implement; pass. **Step 3: Gate and commit** — `git commit -m "feat(portal): password reset and a 24-hour factor reset"`.

### Task 8: Profile and account

**Files:** `internal/portal/profile.go` and test.

**Interfaces:**
- `POST /api/me/display-name` `{displayName}`; `POST /api/me/password` `{currentPassword, newPassword}` (step-up `change-password`, bumps epoch, `password-changed`); `POST /api/me/email` `{newEmail}` (step-up `change-email`; `email-change-confirm` to the new address, nothing else changes yet) and `POST /api/auth/confirm-email` `{token}` (moves `EMAIL#` in one transaction, `409 email_taken` if the new one was taken since, `email-changed` to the old address); `DELETE /api/me` (step-up `delete-account`; deletes the personal workspace's clients through the portal key, then the memberships, the workspace, the email item and the user; `account-deleted` email; the session ends). P2 has no organisations, so the "only owner of an organisation with other members" refusal is P3's.
- `POST /api/me/avatar` `{contentType}` → `{url, fields, key}` (presigned POST); `POST /api/me/avatar/confirm` `{key}` (key must be under `avatars/<own id>/`; HeadObject size ≤ 1 MiB and type in the allowed three, else delete it and `400 invalid_avatar`; sets it and deletes the previous one); `DELETE /api/me/avatar`.

- [ ] **Step 1: Write the failing tests**: `TestChangeEmailNeedsConfirmation` (until confirmed, login still uses the old email); `TestChangeEmailRaceTaken` (another account takes the new email first → `409 email_taken`); `TestDeleteAccountDeletesWorkspaceClients` (fake AuthGate records one `DELETE /api/portal/clients/{id}` per client with the workspace tenant id; the email can sign up again afterwards); `TestAvatarConfirmRejectsOthersKeyAndOversize`.
- [ ] **Step 2:** Implement; pass. **Step 3: Gate and commit** — `git commit -m "feat(portal): profile, email change, account deletion and avatars"`.

### Task 9: The personal workspace's clients and audit

**Files:** `internal/portal/{tenants.go,clients.go}` and tests.

**Interfaces:**
- `GET /api/tenants` → `{tenants:[{tenantId, kind, name, role}]}` (P2: the personal one).
- Under `a.member(...)` (Task 3) and `StageFull`: `GET|POST /api/t/{tenantId}/clients`, `GET|PATCH /api/t/{tenantId}/clients/{id}`, `POST …/{id}/disable|enable`, `POST …/{id}/keys`, `DELETE …/{id}/keys/previous`, `DELETE …/{id}/subjects/{subjectId}`, `DELETE …/{id}` — each calls the matching `authgate.TenantClient` method with that tenant id and passes AuthGate's JSON and 4xx codes through (`webapi.AuthGateError`); step-up per Global Constraints; roles per spec section 6 (in P2 the owner can do all). `GET /api/t/{tenantId}/audit?cursor=`.
- Every write is audited in the tenant's audit (actor user id and display name, action, target, IP via `viewerip`).

- [ ] **Step 1: Write the failing tests**: `TestOtherUsersWorkspaceIs404` (user B on A's tenant id, and an unknown tenant id → identical `404 not_found`, zero AuthGate calls); `TestClientCallsCarryTheWorkspaceTenant` (fake AuthGate sees the portal key and `x-tenant-id`); `TestQuotaAndSuspensionPassThrough` (AuthGate `409 quota_exceeded`/`tenant_suspended` reach the client unchanged); `TestClientWritesAreAuditedWithIP`; `TestRotateNeedsStepUp`.
- [ ] **Step 2:** Implement; pass. **Step 3: Gate and commit** — `git commit -m "feat(portal): clients and audit in the personal workspace"`.

### Task 10: portal-web, sign-up to second factor

**Files:** `portal-web/` (Vite app on the web kit; `vite.config.ts` dev proxy `/api` → `http://localhost:8789`; the CSP of Task 12 in preview), screens `SignUp`, `CheckEmail`, `VerifyEmail`, `SignIn`, `SecondFactor`, `Recovery`, `Enroll` (PIN or passkey, then the recovery codes with `ShownOnce`-style confirm "I have saved them"), `Forgot`, `ResetPassword`, `FactorReset` (request and confirm), session provider and guards, Turnstile widget component (loads `https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit`, site key from `GET /api/config`), `strings.ts` (EN and VI, typed like the Console's).

- [ ] **Step 1: Tests (vitest)**: strings complete in both languages (the Console's strings test pattern); `normaliseCode` matches the backend's for the same inputs; the session guard sends `half` to `/sign-in/second-factor` and `enroll` to `/enroll`.
- [ ] **Step 2:** Implement; `pnpm --filter portal-web run typecheck && pnpm --filter portal-web test && pnpm --filter portal-web run build`.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(portal-web): sign-up, verification, sign-in, enrolment and recovery"`.

### Task 11: portal-web, account and workspace

**Files:** screens `Account` (profile, avatar upload via the presigned POST then confirm, password, email change, factors, recovery codes, delete account with `TypeToConfirm` of the email then step-up), `Clients`, `ClientNew`, `ClientDetail` (rotate, revoke previous, disable/enable, purge subject, delete with `TypeToConfirm`; "Locked by an administrator" when `disabledBy=operator`), `Audit`; shell with the tenant switcher (one entry in P2); routes `/t/:tenantId/clients…`.

- [ ] **Step 1: Tests (vitest)**: client list renders the empty state and a capped list; the delete flow needs the exact name; an operator-locked client shows no Enable action.
- [ ] **Step 2:** Implement; typecheck, test, build. **Step 3: Gate and commit** — `git commit -m "feat(portal-web): account, clients and audit"`.

### Task 12: Infrastructure, scripts, smoke and CI

**Files:**
- Create: `infra/portal.yaml` (the Console's template shape: function `auth-gate-portal-${Stage}` with env from Task 3, table, site bucket, avatar bucket `auth-gate-portal-${Stage}-avatars-${AccountId}` (blocked public access, CORS allowing POST from the Portal's origin), CloudFront with `/api/*`, `/avatars/*` (origin the avatar bucket through OAC, GET/HEAD only) and the SPA default, tag `app=auth-gate`; site CSP = the Console's plus `script-src 'self' https://challenges.cloudflare.com`, `frame-src https://challenges.cloudflare.com`, `connect-src 'self' https://<avatar bucket regional domain>`, `img-src 'self' data:`; function policy: its table, `ssm:GetParameter` on its three parameters and the portal key, `ses:SendEmail` on `arn:aws:ses:${AWS::Region}:${AWS::AccountId}:identity/*` with condition `ses:FromAddress = EMAIL_FROM`, `s3:PutObject|GetObject|DeleteObject` on `<avatar bucket>/avatars/*`; parameters `Stage`, `AuthGateBaseUrl`, `AuthGateClientId`, `PublicBaseUrl`, `EmailFrom`, `TurnstileSiteKey`).
- Create: `scripts/portal/{lib.sh,deploy.sh,register-client.sh,smoke.sh,purge.sh}` (copy the Console's; `deploy.sh` two-pass for the client id and for `PublicBaseUrl` read from its own `BaseUrl` output; `register-client.sh stg|local` writes `authgate-client-key`; `smoke.sh` checks the SPA shell and CSP (no `unsafe-inline`, has the Turnstile host), `/api/nope` 404, the raw Function URL 401, `GET /api/config` 200, and runs `bruno-portal/01-boundary`), `bruno-portal/01-boundary/` (unknown path 404, text/plain 415, login unknown 401 `invalid_credentials`, signup without Turnstile 403, `/api/me` without cookie 401, `/api/t/<ulid>/clients` without cookie 401).
- Modify: `infra/role-boundary.json` (`ReadParameters` gains `parameter/credo/auth-gate-portal/*`; new `SendPortalEmail` `ses:SendEmail` on `identity/*`; new `PortalAvatars` `s3:PutObject|GetObject|DeleteObject` on `arn:aws:s3:::auth-gate-portal-*/avatars/*`), `infra/deployer-policy.json` (`ManageTheConsoleSite` becomes `ManageTheSiteBuckets` over `auth-gate-console-*` and `auth-gate-portal-*`; `ManageTheParameters` gains `auth-gate-portal/*`), `scripts/ci/preflight.sh` (the Portal stack and its `AuthGateClientId`), `.github/workflows/ci.yml` (`scripts/portal/deploy.sh` and `smoke.sh` after the Console's), `package.json` lint (`scripts/portal/*.sh`), `scripts/setup-and-deploy/06-prune-clients.sh` (propose only clients without an `owner`; test case).

- [ ] **Step 1:** `sam validate --lint -t infra/portal.yaml`; both policy files under 6144 non-whitespace characters; `shellcheck`; the prune test passes with the new owner case.
- [ ] **Step 2:** Local end to end: `make dev` (AuthGate), `scripts/portal/register-client.sh local`, `make portal-dev`, `pnpm --filter portal-web dev`; sign up with the test Turnstile key, open the link from `MAILBOX_DIR`, enrol a PIN, save the codes, create a client, call AuthGate's verify API with its key (`GET /verify/api/factors/x` answers 200 with no factors); `bru run 01-boundary` from `bruno-portal` against it.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(infra): the portal stack, its scripts, smoke and ci"`.

### Task 13: Docs and rollout

**Files:** `docs/memo/portal-accounts.md` (sign-up without enumeration, mandatory second factor and recovery codes, the lazy 24-hour factor reset, Turnstile adaptive on login, HIBP fail-open, presigned avatar upload and server confirm); `README.md` (the Portal section, local development, the rollout below); spec fixes carried from P1: §4.3 says the Console (not AuthGate) answers `409 own_client_protected`, and the `ErrDisabledByOperator` doc comment in `internal/auth/client/client.go` mentions delete.

- [ ] **Step 1: The rollout**, paste-ready, in order, before the merge, from this worktree:
  1. SES: `aws sesv2 create-email-identity --email-address <your address>` and click the link; `aws sesv2 put-account-suppression-attributes --suppressed-reasons BOUNCE COMPLAINT`.
  2. Turnstile: create a widget for the Portal hostname (the CloudFront domain after the first deploy; until then `localhost`) in the Cloudflare dashboard; store the secret with a paste-safe command (`read -rs` then a 0600 file and `--cli-input-json`) as `/credo/auth-gate-portal/stg/turnstile-secret`.
  3. `ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh` then `ADMIN_PROFILE=default scripts/ci/00-ci-role.sh` (boundary and policy changes), back to back.
  4. First Portal deploy under the admin profile, two-pass: `AWS_PROFILE=default EMAIL_FROM=<address> TURNSTILE_SITE_KEY=<key> scripts/portal/deploy.sh`, `scripts/portal/register-client.sh stg`, then deploy again with `AUTHGATE_CLIENT_ID=<id>`; add the CloudFront hostname to the Turnstile widget; `scripts/portal/smoke.sh`.
  5. Merge; CI deploys all three and runs the three smokes.
  6. Browser run: sign up with the SES-verified address, verify, enrol, save codes, create a client, rotate its key, sign out and in with a recovery code, reset the password.
- [ ] **Step 2: Gate and commit** — `git commit -m "docs: the portal's accounts, and the P2 rollout"`.
