# AuthGate Console — design

Date: 2026-11-06. Status: approved. Lives on the `orchestrator` branch of auth-gate
until the feature branch exists; the first task there copies it to
`docs/superpowers/specs/2026-11-06-console-design.md`.

## 1. Purpose

Onboarding a system onto AuthGate today means running a script with the admin key. The
Console is the operator's screen for that work: register and manage clients, rotate keys,
disable a client, and clear one user's factors. It is what lets many systems use AuthGate
without each one going through a shell.

Decided by the product owner on 2026-11-06 (not re-askable):

- It is a separate app from the auth-app docs and demo, with its own URL.
- It lives in the auth-gate repository, in its own stack.
- The browser never holds the admin key. A Console backend reads it from SSM.
- Accounts are created by an operator; there is no public sign-up.
- Sign-in is password plus a PIN or passkey verified by AuthGate itself.
- The UI is bilingual (EN/VI).
- The backend is Go.
- Admin accounts are managed from a screen, by a master admin.
- PIN or passkey is asked at sign-in, and again only for security-sensitive actions.
- Rate limiting with a token bucket comes now because it has no fixed cost; WAF follows when budget allows.

## 2. Non-goals (v1)

Roles beyond master and admin, SSO, password reset by email, usage metrics, hard-deleting a
client, browsing a client's users, a custom domain being required. WAF is deferred for cost, and
until it lands nothing limits requests that have no valid client key, and there is no IP allow-list.

## 3. Architecture

```
browser ──► CloudFront (Console URL)
              ├─ /api/*  → Lambda Function URL (console, Go, arm64; x-origin-verify)
              └─ /*      → S3 (SPA; CloudFront Function rewrites routes to /index.html)
console ──► DynamoDB auth-gate-console-<stage>   (admins, sessions, used tokens, audit)
console ──► AuthGate /api/clients*               (admin key, from SSM)
console ──► AuthGate /api/challenges, /factors…  (its own client key, from SSM)
console ──► AuthGate /.well-known/jwks.json      (verifies tokens, cached 300 s)
```

Stack `auth-gate-console-<stage>`, separate from `auth-gate-<stage>`, so a Console deploy
cannot touch the service and the service never gains a browser-facing path.

Layout in the repository:

```
cmd/console/             Lambda entry, plus `console admin …` operator subcommands
internal/console/        accounts, sessions, audit, authgate client, HTTP handlers
console-web/             React + Vite + Mantine SPA, typed EN/VI strings
infra/console.yaml       SAM template
scripts/console/         deploy, register-client, create-admin, purge
```

Rules:

- `cmd/console` is its own binary and does not import `internal/auth/service`. It reaches
  AuthGate only over HTTP, like any other client. That keeps the KMS and table permissions
  out of a browser-facing Lambda.
- The Console is a registered AuthGate client with its own `rpId` and `origins`, set up with
  the same two-pass deploy as auth-app.
- Visual language follows auth-app: same green palette, glass panels and type. No shared
  package in v1; the two repos copy.

## 4. Changes to AuthGate

The admin API today offers create, get one, rotate key and purge subject. The Console needs:

| Route | Body | Result | Notes |
|---|---|---|---|
| `GET /api/clients` | `?cursor=` | `200 {clients: [...], nextCursor?}` | Summaries, no key material. |
| `PATCH /api/clients/{id}` | any of `name, origins, pinPolicy, tokenTtlSec, rateLimit` | `200` client view | `rpId` cannot change: existing passkeys are bound to it. Origins still pass the `rpId` suffix check. |
| `POST /api/clients/{id}/disable` | `{}` | `200` client view | Sets `disabledAt`. |
| `POST /api/clients/{id}/enable` | `{}` | `200` client view | Clears it. |

Decisions:

- **Listing uses an index item, not a scan or a GSI.** `pk=CLIENTS`, `sk=C#<clientId>`
  holding name, rpId, createdAt, disabledAt, written in one transaction with the client item.
  Existing clients need a one-off backfill, run once per stage. Clients created by smoke runs
  will appear, so the list screen filters by name.
- **Update is a conditional `UpdateItem`.** `PutClient` overwrites the whole item, which would
  lose `keyHashes`.
- **Disable is checked at key lookup.** A disabled client's keys answer `401`, with the same body
  as an unknown key. Tokens already issued stay valid until `exp` (900 s at most) because
  verification is offline; the UI says so.
- Rotate, get and purge are unchanged. `/api/clients*` already routes to the admin role.
- The Bruno boundary folder and `cmd/smoke` gain the disable case.

## 5. Rate limiting

**AuthGate, per client.** A token bucket on the `verify` role, checked right after the key lookup,
keyed by clientId. Admin and JWKS routes are not limited.

- Item `RL#<clientId>` / `META`: `tokens`, `updatedAt`. Lambda instances share nothing, so the
  bucket has to live in the table.
- `updatedAt` marks the start of the refill window nobody has claimed yet. Each request reads the
  item and computes `have = min(burst, tokens + elapsed * perSecond)`. Fewer than one token: `429`,
  with no write.
- The first writer on an unclaimed window claims it, even when it earns nothing: with
  `refill = have - tokens`, it writes `tokens + (refill - 1)` and `updatedAt = max(now, read + 1)`
  with the condition `updatedAt = <the value read> AND tokens >= 1 - refill`.
- A writer that lost the claim, or found no window to claim, spends with `tokens - 1` and the
  condition `tokens >= 1`. Lost that too: `429`. There is no retry loop.
- Product owner's decision, 2026-11-06: this replaces the earlier condition and retry rule. On a
  full bucket of 100, 100 simultaneous calls allowed only 11 to 22 under them, so callers got `429`
  while tokens remained.
- `429 {"error": "rate_limited"}` with `Retry-After` in whole seconds.
- A DynamoDB error on the limiter logs and lets the request continue. The request needs the same
  table anyway, so failing here adds no protection.
- Settings on the client: `rateLimit {burst, perSecond}`, default burst 100 and 50 per second,
  changed through `PATCH /api/clients/{id}`. `perSecond: 0` is rejected; there is no unlimited mode.

Cost: one extra read and at most two writes per allowed request, billed by use, so no fixed monthly
charge. A failed conditional write still consumes write capacity. Known limit: one item still has
DynamoDB's per-item write limit. The build measures the default against DynamoDB Local with
concurrent callers; shards are the next step if a client outgrows it.

**Console, per IP.** The same bucket on `/api/auth/*`, in the Console table, burst 10 and 1 per 6
seconds. The IP comes from `CloudFront-Viewer-Address` through an origin request policy, because
a caller can forge the left of `X-Forwarded-For`. Account lockout (§6) still applies.

**auth-app follow-up.** Its `bff` maps unknown 4xx to `502 authgate_unavailable`, so it must map
`rate_limited` to `429` with a message, and its `errors` docs page gains a row.

## 6. Sign-in and accounts

Table `auth-gate-console-<stage>`, string `pk`/`sk`, TTL attribute `ttl`, on-demand, PITR on.

| Item | `pk` | `sk` | Attributes |
|---|---|---|---|
| Admin | `ADMINS` | `A#<username>` | adminId (ULID, the AuthGate `subjectId`), role (`master`, `admin`), passwordHash (argon2id), mustChangePassword, failedCount, lockedUntil, disabledAt, createdAt |
| Session | `SES#<sha256(token)>` | `META` | adminId, username, stage (`half`, `enroll`, `full`), createdAt, lastSeenAt, `ttl` |
| Used token | `JTI#<jti>` | `META` | `ttl` = token `exp` |
| Audit | `AUDIT#<yyyy-mm>` | `<timestamp>#<ULID>` | adminId, username, action, target (client, subject or admin), result, ip |
| Rate limit | `RL#<ip>` | `META` | tokens, updatedAt, `ttl` |

Flow:

1. **Password.** Unknown username and wrong password answer the same `401 invalid_credentials`,
   and the hash compare runs either way. Five failures lock the account for 15 minutes.
2. **First sign-in.** Session stage `enroll`: only changing the temporary password, enrolling a
   factor and signing out are allowed. An admin cannot reach any other screen until both are done.
3. **Second factor.** Session stage `half` for 10 minutes. `POST /challenges` with purpose
   `console-login`, then verify.
4. **Token check.** Signature, `iss`, `aud` = the Console's clientId, `sub` = adminId, purpose,
   `exp`, and `jti` single use through a conditional put on `JTI#`.
5. **Full session.** 30 minutes idle, 8 hours absolute.

Sessions are server-side, unlike auth-app, so disabling an admin or signing out ends the session
at once. Cookie `__Host-console_session`: opaque random token, HttpOnly, Secure, SameSite=Strict.
Non-GET requests must be `application/json`.

**Roles.** `admin` manages clients and reads the audit log. `master` also manages admin accounts.

**Step-up only on security-sensitive actions.** Once signed in, routine work asks for nothing more.

| Asks for PIN or passkey again | Does not |
|---|---|
| Rotate a client key | Viewing clients and the audit log |
| Disable a client | Creating a client |
| Clear one user's factors | Renaming a client |
| Change a client's origins, PIN policy, token TTL or rate limit | Enabling a client |
| Change your own password | Enrolling your first factor |
| Add or remove your own PIN or passkey, once you have a factor | |
| Every action on another admin's account | |

Each has its own purpose (`console-rotate-key`, …). The frontend attaches the token to the action. It
must match the admin and action and is single use, stricter than auth-app's sample, where a replay
works until `exp`.

**Managing admins (master only).**

- Create an admin: a temporary password is shown once, and they set their own at first sign-in.
- Reset a password: same temporary password flow.
- Clear an admin's factors, so they enroll again at next sign-in.
- Disable or enable, and change role.
- Resetting, clearing or disabling ends that admin's sessions at once.
- You cannot disable or demote yourself, and the last active master cannot be disabled or demoted.

**Self-service:** change password (asks for the current one), add or remove PIN and passkeys. The
last remaining factor cannot be removed.

**Operator commands** (`console admin create|reset-password|disable|clear-factors`) create the first
master and recover a locked-out one. They read secrets from stdin, never argv, and write to the table
with the operator's AWS profile. A forgotten password goes to a master, or to these commands if the
master is the one locked out, because there is no email system.

**If AuthGate is down,** nobody can sign in to the Console. The scripts that use the admin key
directly remain the documented emergency path.

## 7. Backend routes

All under `/api`, JSON. Error envelope `{"error": "<snake_case>", ...}`.

| Route | Session | Notes |
|---|---|---|
| `POST /auth/login` | none | `{next: "second-factor" \| "enroll", factors?}` |
| `POST /auth/second-factor/start`, `/verify` | half | |
| `POST /auth/logout` | any | Deletes the session item. |
| `GET /me` | any | `{username, stage, factors}` |
| `POST /me/password` | enroll, full + step-up | Current and new password. No step-up in `enroll`. |
| `POST /me/pin`, `DELETE /me/pin` | enroll, full + step-up | Delete refused for the last factor. No step-up in `enroll`. |
| `POST /me/passkeys/start`, `/finish`, `DELETE /me/passkeys/{id}` | enroll, full + step-up | No step-up in `enroll`. The session moves to `full` once the password is changed and a factor exists. |
| `POST /stepup/start`, `/verify` | full | Action must be in the allow-list. |
| `GET /clients`, `GET /clients/{id}` | full | |
| `POST /clients` | full | Key returned once. |
| `PATCH /clients/{id}` | full; + step-up unless only `name` changes | |
| `POST /clients/{id}/keys` | full + step-up | Key returned once. |
| `POST /clients/{id}/disable` | full + step-up | The Console's own client cannot be disabled. |
| `POST /clients/{id}/enable` | full | |
| `DELETE /clients/{id}/subjects/{subjectId}` | full + step-up | |
| `GET /audit` | full | Newest first, by month. |
| `GET /admins` | master | |
| `POST /admins` | master + step-up | Temporary password returned once. |
| `POST /admins/{username}/reset-password`, `/clear-factors`, `/disable`, `/enable`, `/role` | master + step-up | Guards in §6. Other roles get `403 forbidden`. |

Every write records an audit item, success or failure. Audit items and logs never contain a key,
PIN, password or token. Responses carrying a key are `Cache-Control: no-store`.

## 8. Screens

Sign in, second factor, first-run enrollment, clients (list, name filter, status), create client,
client detail (edit, rotate, disable or enable, clear one user's factors), key shown once (copy,
must confirm it was saved before closing), audit log, my account (password, PIN, passkeys), admins
(master only: list, create, temporary password shown once, reset, clear factors, disable, role).
Destructive actions state the consequence: rotating keeps the old key valid for 24 hours;
disabling stops the client's calls at once; clearing factors cannot be undone; the purge takes a
typed subject id. Every string in EN and VI, through a typed strings module so a missing key fails
typecheck. Dates follow the language.

## 9. Infrastructure

`infra/console.yaml` follows auth-app's stack: bucket with OAC, one distribution, `x-origin-verify`
to a Function URL. Differences:

- The Lambda role may read `/credo/auth-gate/<stage>/admin-key` plus its own parameters under
  `/credo/auth-gate-console/<stage>/`: `origin-verify` and `authgate-client-key`.
- No CORS, no trace feature.
- A strict security header policy on the site. The inline theme script is replaced by a file so the
  CSP needs no `unsafe-inline`.

The admin key is read at cold start, so a future rotation of it must redeploy both stacks.

Build and deploy order: AuthGate rate limiting, which can ship alone, then the other AuthGate changes and backfill, then
the Console stack (first pass), register the Console client, second pass, `console admin create`
for the first master.

## 10. Testing

- Go tests with DynamoDB Local, like the service: login enumeration and lockout, each session stage
  against each route group, token check failures (wrong `aud`, `sub`, purpose, expired, replayed
  `jti`), step-up required exactly on the §6 list, role checks on `/admins`, last-master and
  last-factor refusal, sessions ended on reset or disable, audit written on failure, secrets absent
  from audit.
- Rate limit: refill over time with an injected clock, `Retry-After`, one client's bucket not touching
  another's, and N concurrent callers never exceeding the burst.
- AuthGate: list and backfill, `PATCH` keeps `keyHashes` and rejects `rpId`, disabled key gets `401`.
  A two-client isolation test also goes in `internal/auth/handler`; on 2026-11-06 one was run from a
  throwaway copy and passed, but none is committed.
- Bruno folder for the Console, plus a stage smoke script: deep link gives 200 HTML, `/api/nope` gives
  404 JSON, raw Function URL gives 401.
- A manual browser run covers passkeys, in both languages and both color schemes.

## 11. Confirmed defaults

Proposed in this draft and accepted by the product owner on 2026-11-06.

1. The step-up list in §6.
2. Two roles, with `admin` managing clients and only `master` managing accounts.
3. Rate limit defaults: AuthGate burst 100 and 50 per second per client; Console login burst 10 and
   1 per 6 seconds per IP.
4. A caller that loses its conditional writes because no token is left gets `429` rather than being
   let through. Amended by the product owner on 2026-11-06 with §5's atomic spend: there are no
   repeated attempts.
