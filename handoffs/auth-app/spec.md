# auth-app — design

Date: 2026-10-20. Status: draft for review. Lives on the `orchestrator` branch of
auth-gate until the `auth-app` repository is bootstrapped; the first task there copies it
to `docs/superpowers/specs/2026-10-20-auth-app-design.md`.

## 1. Purpose

auth-app is the integration docs for auth-gate and the sample application that proves
them, published as one site at one URL. Its readers are backend developers who are about
to integrate auth-gate and do not like reading docs. So the docs are short, bilingual
(EN/VI), written in a human voice, and every flow has a "Try it" button that opens the
live demo with a trace panel showing the exact requests the sample backend sent to
auth-gate and what came back.

The sample backend (`bff`) is a reference implementation of a *client backend* in the
auth-gate sense: it owns users and sessions, relays PIN and WebAuthn to auth-gate, and
verifies the token it gets back before trusting it. Developers copy from it.

Decided in brainstorming on 2026-10-20 (not re-askable):

- Readers are integrators, not CV reviewers. Docs are concise, EN/VI, human tone.
- Stack follows the hub pattern (React + Vite + Mantine frontend, Hono backend on
  Lambda) with one deliberate simplification: no separate CRUD service; `bff` reads and
  writes its own table.
- All three auth-gate scenarios are demonstrated: 2FA after password, passkey-only
  login, step-up before a sensitive action. Plus enrollment.
- Everything on AWS: S3 + CloudFront + Lambda Function URL + DynamoDB, one SAM stack,
  the same shape as auth-gate.
- The repository uses the auth-gate worktree tooling (bare repo, orchestrator,
  handoffs) per `docs/infra/06-bootstrap-new-project.md`, with its own orchestrator.
- One site, two faces: `/docs/*` and `/app/*` in one SPA. The auth-gate README gets one
  line linking to the site.
- UI direction comes from the `impeccable` skill, not `frontend-design`.

## 2. Non-goals (v1)

Email verification, password reset, account recovery, remember-device, rate limiting,
discoverable (username-less) passkeys, an admin UI, i18n for the demo screens (demo is
EN only; only the docs are bilingual), running auth-gate locally.

## 3. Architecture

```
browser ──► CloudFront (one URL)
              ├─ /api/*   → Lambda Function URL  (bff: Hono, Node 22, arm64)
              └─ /*       → S3 (SPA: docs + demo; 403/404 → /index.html 200)
bff ──► DynamoDB auth-app-<stage>          (users)
bff ──► auth-gate <stage>  /api/...        (x-internal-access-key: client key, from SSM)
bff ──► auth-gate /.well-known/jwks.json   (cached 300 s) to verify returned tokens
```

Rules kept from the hub pattern: the frontend knows one base URL (`/api`), the backend
has no CORS, and the Function URL refuses calls that do not carry the CloudFront
`x-origin-verify` header (same mechanism as auth-gate).

Repository layout (pnpm workspace; Yarn PnP from hub is dropped to match the worktree
tooling):

```
app/        React 19 + Vite + Mantine SPA: /docs/* and /app/*
bff/        Hono: src/{routes,authgate,users,session,lib}, lambda.ts, server.ts
docs/       Markdown source of the docs pages: en/*.md, vi/*.md (imported at build)
infra/      template.yaml (SAM), deployer-policy.json
scripts/setup-and-deploy/   00-deployer 01-ssm 02-register-client 02-build 03-deploy 04-smoke 99-purge
bruno/      collection against /api of bff
tools/wt  .claude  docs/infra   copied from auth-gate by the bootstrap
```

`docs/*.md` is readable on GitHub, but the site is the primary reading surface because
only it has "Try it" and the trace.

## 4. Backend `bff`

### 4.1 Data

One table `auth-app-<stage>`, string `pk`/`sk`, on-demand. Two item types, written
together in one transaction:

| `pk` | `sk` | Attributes |
|---|---|---|
| `U#<username>` | `META` | userId (ULID), email, passwordHash, createdAt |
| `I#<userId>` | `META` | username (lookup for the session's `sub`) |

`subjectId` sent to auth-gate is `userId`. Passwords are hashed with Node's built-in
`crypto.scrypt` (no native dependency on Lambda).

### 4.2 Session

HttpOnly, Secure, SameSite=Lax cookie holding an HS256 JWT signed with `SESSION_SECRET`
(SSM). Claims: `{sub: userId, mfa: boolean, exp}`. After a correct password, a user who
has at least one factor gets `mfa=false` and may only call the second-factor routes;
`mfa=true` is required for everything under `/me` and `/stepup`. No session table.

### 4.3 Routes

All under `/api`, JSON bodies. "Calls auth-gate" names the auth-gate route relayed.

| Route | Body | Calls auth-gate | Notes |
|---|---|---|---|
| `POST /auth/register` | `{username, password, email}` | — | 201; 409 `username_taken` |
| `POST /auth/login` | `{username, password}` | — | `{next: "done"}` with `mfa=true`, or `{next: "second-factor", factors: {pin, webauthn}}` with `mfa=false` |
| `POST /auth/second-factor/start` | `{factor}` | `POST /challenges` purpose `login` | `{challengeId, expiresAt, publicKey?}` |
| `POST /auth/second-factor/verify` | `{challengeId, pin \| credential}` | `POST /challenges/{id}/verify` | verifies the token (§4.4), re-issues cookie with `mfa=true` |
| `POST /auth/passkey/start` | `{username}` | `POST /challenges` purpose `login`, factor `webauthn` | passkey-only login; username is required because auth-gate v1 has no discoverable credentials. Unknown username → same shape as a user without passkeys (409 `factor_not_enrolled`) to avoid enumeration |
| `POST /auth/passkey/verify` | `{challengeId, credential}` | `POST /challenges/{id}/verify` | full session |
| `POST /auth/logout` | — | — | clears cookie |
| `GET /me` | — | `GET /factors/{subjectId}` | `{username, email, factors: {pin, webauthn: [...]}}` |
| `POST /me/pin` / `DELETE /me/pin` | `{pin}` | `POST /factors/pin`, `DELETE /factors/pin/{subjectId}` | |
| `POST /me/passkeys/start` | `{displayName?}` | `POST /webauthn/registrations` | `{registrationId, publicKey}` |
| `POST /me/passkeys/finish` | `{registrationId, credential}` | `POST /webauthn/registrations/{id}` | `{credentialId}` |
| `DELETE /me/passkeys/{credentialId}` | — | `DELETE /webauthn/credentials/{id}?subjectId=` | |
| `POST /stepup/start` | `{purpose, factor}` | `POST /challenges` with that purpose | `purpose` must be in an allow-list (`change-email`) |
| `POST /stepup/verify` | `{challengeId, pin \| credential}` | `POST /challenges/{id}/verify` | verifies the token, then **returns it to the frontend** as `stepUpToken` |
| `POST /me/email` | `{email, stepUpToken}` | — | verifies `stepUpToken` with `purpose=change-email` and `sub=userId`, then updates |

Step-up deliberately hands the token to the frontend, which attaches it to the action.
That is what the token is for: it proves one verification for one purpose. The
alternative, a flag in the session, is what the docs tell readers not to do.

### 4.4 Token verification (`bff/src/authgate/token.ts`)

Uses `jose` with a remote JWKS at `<AUTHGATE_BASE_URL>/.well-known/jwks.json`, cached
300 s. Checks: signature, `iss == AUTHGATE_BASE_URL`, `aud == AUTHGATE_CLIENT_ID`,
`exp`, `sub == expected subjectId`, `purpose == expected purpose`. Returns the claims.
This is the module readers copy, so it has no dependency on the rest of `bff`.

### 4.5 auth-gate client (`bff/src/authgate/client.ts`)

One `fetch` wrapper that adds `x-internal-access-key`, parses JSON, maps errors (§4.7)
and records a trace entry per call.

### 4.6 Trace

When `TRACE=1`, every route that called auth-gate adds `trace: [{method, path,
requestBody, status, responseBody, ms}]` to its response, success or error. Redaction is
applied before storing: `pin` → `"***"`, `credential` → `{...redacted}`, `token` →
first 16 chars + `…`, and the access-key header is never recorded. The docs state that
trace is a demo feature and must not ship in a real client.

### 4.7 Error mapping

| auth-gate | bff | Frontend |
|---|---|---|
| 400 `verification_failed` + `attemptsRemaining` | 400 same, keeps `attemptsRemaining` | "Wrong PIN, N attempts left" |
| 423 `locked` + `lockedUntil` | 423 same | disable form, show unlock time |
| 410 `challenge_gone` | 410 same | "Expired, try again", restart automatically |
| 404 `factor_not_found` | 409 `factor_not_enrolled` | link to `/app/security` |
| 400 `registration_failed` | 400 same | "Could not register this passkey" |
| 5xx or network | 502 `authgate_unavailable` | generic notice |

### 4.8 Configuration

Env: `AUTHGATE_BASE_URL`, `AUTHGATE_CLIENT_ID`, `TABLE`, `STAGE`, `TRACE`. From SSM at
cold start: `/auth-app/<stage>/session-secret`, `/auth-app/<stage>/origin-verify`,
`/auth-app/<stage>/authgate-client-key`. Locally these come from `bff/.env`.

## 5. Frontend `app`

React 19, Vite, Mantine, react-router. Light and dark theme. Visual direction is set by
the `impeccable` skill in the implementing worktree; the brief asks for a human tone, no
hero banners, gradients or emoji, monospace for code and trace.

### 5.1 Demo `/app/*`

| Screen | Scenario | Behaviour |
|---|---|---|
| `/app/register`, `/app/login` | 2FA after password | on `next: "second-factor"` show a PIN or passkey step |
| `/app/login/passkey` | passkey-only | username → `navigator.credentials.get` |
| `/app/home` | | greeting, email with "Change" (triggers step-up) |
| `/app/security` | enrollment | set/remove PIN, add/remove passkeys (`navigator.credentials.create`) |
| step-up modal | step-up | choose PIN or passkey → verify → send `stepUpToken` with the new email |

`app/src/lib/webauthn.ts` does exactly two things: base64url ↔ ArrayBuffer for the
creation/request options and for the resulting credential. No library, so readers see
all of it.

### 5.2 Trace panel

A right-hand drawer opened by a floating button. Each response that carries `trace`
appends entries: one line `POST /api/challenges → 201 (84 ms)`, expandable to request
and response JSON. Buttons: Clear, Copy as curl (key as a placeholder). "Try it" links
from the docs open the demo with the drawer already open.

### 5.3 Docs `/docs/*`

Source: `docs/en/<page>.md` and `docs/vi/<page>.md`, same file names, frontmatter
`title` and optional `tryIt: /app/...`. Loaded with Vite `import.meta.glob(..., {query:
'?raw'})`, rendered with `marked`; the two frontmatter keys are parsed by a ten-line
function, no frontmatter library. Language toggle EN/VI in the header, stored in
`localStorage`, URL unchanged. Each page fits one screen.

| Page | Content |
|---|---|
| `what-it-is` | ten lines: what auth-gate does and does not do, one diagram |
| `setup` | register a client with the admin key, keep the client key, `rpId`/`origins` |
| `enroll` | PIN and passkey enrollment, Try it → `/app/security` |
| `2fa-login` | sequence diagram, backend relay snippet, browser snippet, Try it |
| `passkey-login` | same shape |
| `step-up` | same shape, plus why the token goes with the action |
| `verify-token` | the `token.ts` snippet and the table of claims to check |
| `errors` | status/`error` → what to do |

## 6. Infra and deploy

SAM stack `auth-app-<stage>`:

| Resource | Notes |
|---|---|
| Lambda `auth-app-<stage>-bff` | Node 22, arm64, Function URL `AuthType: NONE`, checks `x-origin-verify` |
| S3 bucket + OAC | holds `app/dist`; only CloudFront may read |
| CloudFront | `/api/*` → Function URL (no cache, all viewer headers except Host); `/*` → S3; S3 403/404 → `/index.html` 200 |
| DynamoDB `auth-app-<stage>` | on-demand, pk/sk |
| SSM parameters | `session-secret`, `origin-verify`, `authgate-client-key` under `/auth-app/<stage>/` |

Scripts in `scripts/setup-and-deploy/`, numbered like auth-gate; `STAGE` (default
`stg`), `AWS_PROFILE` (default `auth-app-deployer`), `AWS_REGION` (default
`ap-southeast-1`):

0. `00-deployer.sh` — IAM user `auth-app-deployer` scoped to `auth-app-*`.
1. `01-ssm.sh` — random `session-secret` (SecureString) and `origin-verify` (String).
2. `02-register-client.sh [stg|local]` — `POST /api/clients` on auth-gate with the admin
   key: `stg` uses `rpId` = the CloudFront domain and `origins` = `https://<domain>`,
   writing the client key to SSM; `local` uses `rpId: localhost`, `origins:
   http://localhost:5173`, writing to `bff/.env`. Needs the auth-gate admin key in
   `AUTHGATE_ADMIN_KEY`.
3. `02-build.sh` — `pnpm -r build`, zip `bff`.
4. `03-deploy.sh` — `sam deploy`, `aws s3 sync app/dist`, CloudFront invalidation, prints
   one URL. First deploy has no client key yet: deploy, read the domain, run
   `02-register-client.sh stg`, deploy again. Same two-pass trick as auth-gate.
5. `04-smoke.sh` — register → login → set PIN → logout → login + PIN → step-up → change
   email, with curl or a small Node script. Passkeys are not smoked (no headless
   authenticator); PIN proves the wiring.
6. `99-purge.sh` — tear down.

Local: `pnpm dev` runs `bff` with `tsx watch` on `:8787` against DynamoDB Local
(docker compose) and Vite on `:5173` proxying `/api` → `:8787`. auth-gate is always the
deployed stg; running it locally would need a fake KMS.

## 7. Tests

Only where the leverage is; each test says why it exists:

- `bff/src/authgate/token.test.ts` — bad signature, wrong `aud`, wrong `purpose`,
  expired, valid. Why: readers copy this file; a bug here is a hole on their side.
- `bff/src/session.test.ts` — `mfa=false` cookie cannot reach an `mfa=true` route. Why:
  the only gate of 2FA.
- `bff/src/lib/trace.test.ts` — `pin`, `credential`, `token`, access key never appear
  in trace output. Why: trace is on in a public stage.
- `app/src/lib/webauthn.test.ts` — base64url ↔ ArrayBuffer round trip on real option
  and credential fixtures (from auth-gate `cmd/smoke`). Why: encoding is the most common
  WebAuthn integration bug.

Pure relay routes have no unit tests; the Bruno collection and `04-smoke.sh` cover them.
Gate: `pnpm run check` = lint, typecheck, test, build for both packages.
