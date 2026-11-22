# auth-gate

auth-gate is a two-factor verification service for other backends: a PIN or a WebAuthn
passkey, checked on AWS Lambda, answered with a short-lived token signed by AWS KMS.
A calling backend (a *client*) already knows its user (a *subject*). It asks auth-gate to
enroll and later verify a second factor, and attaches the returned token to whatever the
check was for: a step-up before a sensitive action, the second step of a login, or a
passkey-only login. Browsers never call auth-gate; only backends do.

Operators register and manage clients from the [Console](#the-console), a separate web app
with its own stack. Anyone else signs up at the [Portal](#the-portal), a third app that manages
their own clients. Both reach auth-gate over HTTP like any other client.

The design is in [`docs/superpowers/specs/`](docs/superpowers/specs/2026-10-18-auth-gate-module-design.md),
and the reasons behind each choice are one-page memos in [`docs/memo/`](docs/memo/).

To integrate, start with the [docs and live demo](https://d3d49hg6rb8ylx.cloudfront.net) (EN/VI).

## One base URL

One Go binary runs as three Lambda functions behind one CloudFront distribution, which
routes by path. Integrators see a single base URL, the distribution's `*.cloudfront.net`
name or an optional custom domain (parameters `DomainName`, `CertificateArn`,
`HostedZoneId`).

| Path | Role | Credential |
| --- | --- | --- |
| `/api/clients*`, `/api/owners*` | `admin` | `x-internal-access-key: <admin key>` |
| `/api/portal*` | `admin` | `x-internal-access-key: <portal key>` and `x-tenant-id` |
| `/.well-known/*` | `jwks` | none |
| everything else | `verify` | `x-internal-access-key: <client key>` |

The Function URLs behind the door refuse direct calls: CloudFront adds a secret
`x-origin-verify` header and `verify` and `admin` answer 401 without it. See
[one-door-cloudfront](docs/memo/one-door-cloudfront.md) and
[three-roles-one-binary](docs/memo/three-roles-one-binary.md).

## Credential model

- The **admin key** (SSM SecureString, read at cold start) creates clients and rotates
  their keys.
- The **portal key** (SSM SecureString, `portal-key`) is the Portal's credential. It opens only
  `/api/portal/*`, where every call names a tenant in `x-tenant-id` and reaches only that
  tenant's clients. The admin key does not open those routes and the portal key does not open
  the admin routes: each answers `401 unauthorized` on the other's
  ([memo](docs/memo/client-ownership.md)).
- A **client key** (`ag_` plus 32 random bytes) identifies one calling backend. It is
  returned once; only its SHA-256 is stored, and `clientId` is derived from it, never
  taken from the body ([memo](docs/memo/client-key-is-a-hash-lookup.md)).
- Rotating a client key keeps the previous key valid for 24 hours.
  `DELETE /api/clients/{id}/keys/previous` ends that grace at once and leaves the current key
  alone. Only hashes are stored, so an operator cannot name a leaked key; this route cuts off
  every previous key instead.
- A disabled client's keys answer 401, the same body as an unknown key. Tokens already issued
  stay valid until `exp`, because consumers verify them offline.
- A client key on an admin route is a 401, and the 401 body never names the key or header.

Each client registers its `rpId` and `origins`, so it can only verify passkeys for its
own domains. A subject is an opaque string chosen by the client; ids containing `#` are
rejected. PINs are argon2id hashed and locked after repeated failures.

## Admin API

All under `/api/clients`, with `x-internal-access-key: <admin key>`.

| Route | What it does |
| --- | --- |
| `POST /api/clients` | Create a client; the key is returned once. |
| `GET /api/clients` | List clients, paged with `?cursor=`. Summaries only, no key material. |
| `GET /api/clients/{id}` | One client. |
| `PATCH /api/clients/{id}` | Change `name`, `origins`, `pinPolicy`, `tokenTtlSec` or `rateLimit`. `rpId` cannot change: existing passkeys are bound to it. |
| `POST /api/clients/{id}/disable` | Disable the client. |
| `POST /api/clients/{id}/enable` | Enable the client again. |
| `POST /api/clients/{id}/keys` | Rotate the key; the old one lives 24 hours. A rotate that loses a race with another answers `409 rotate_conflict` and issues nothing. |
| `DELETE /api/clients/{id}/keys/previous` | End every previous key's grace now: `{"revoked": n}`. |
| `DELETE /api/clients/{id}/subjects/{subjectId}` | Purge one subject's factors. |
| `DELETE /api/clients/{id}` | Soft delete, for any client: its keys answer 401 at once, it leaves every list and frees its owner's quota slot. `404` for an unknown or already deleted client. |

The client list reads an index item written with each client. Clients created before it
existed need `05-backfill-clients.sh` once per stage. Each summary carries `owner` (the tenant
id) when a tenant owns the client, and omits it for an operator's.

Tenants are managed under `/api/owners/{tenantId}`, with the admin key. A tenant has no item
until it first creates a client or an operator changes it; then it has the defaults.

| Route | What it does |
| --- | --- |
| `GET /api/owners/{tenantId}` | `clientCount`, `clientQuota` (default 5) and `suspendedAt` when set. |
| `PATCH /api/owners/{tenantId}` | Set `clientQuota`, 0 to 1000. Below the current count, existing clients stay and creates are refused; 0 freezes creation. |
| `POST /api/owners/{tenantId}/suspend` | Refuse the tenant's creates and portal writes, and stop its keys. |
| `POST /api/owners/{tenantId}/unsuspend` | Lift it. |

### Portal API

What the Portal's backend calls, with `x-internal-access-key: <portal key>` and
`x-tenant-id: <ULID>` (a missing or malformed one answers `400 invalid_tenant`). The routes
mirror the admin ones on the tenant's own clients:

| Route | What it does |
| --- | --- |
| `POST /api/portal/clients` | Create a client owned by the tenant; the key is returned once. The rate limit is forced to burst 20, 10 per second, whatever the body says. `409 quota_exceeded` at the quota. |
| `GET /api/portal/clients` | List the tenant's clients, paged with `?cursor=`. |
| `GET /api/portal/clients/{id}` | One client. |
| `PATCH /api/portal/clients/{id}` | Change `name`, `origins`, `pinPolicy` or `tokenTtlSec`. `rateLimit` answers `400 invalid_client`. |
| `POST /api/portal/clients/{id}/disable`, `.../enable` | Disable or enable. Enable answers `409 disabled_by_operator` for a client an operator disabled. |
| `POST /api/portal/clients/{id}/keys` | Rotate the key. |
| `DELETE /api/portal/clients/{id}/keys/previous` | End every previous key's grace now. |
| `DELETE /api/portal/clients/{id}/subjects/{subjectId}` | Purge one subject's factors. |
| `DELETE /api/portal/clients/{id}` | Soft delete; frees a quota slot. `409 disabled_by_operator` for a client an operator disabled. |

A client of another tenant, of an operator, a deleted one and an unknown id all answer
`404 client_not_found`. While the tenant is suspended every write answers
`409 tenant_suspended` at once (except a write already in flight when the suspension lands: a portal write reads the counters, then writes; only a create is atomic with them), and its clients' keys stop working within 30 seconds (each
instance caches the tenant's state that long); an operator's clients never read it.

### Deleting clients

A delete is soft, so the factors (PINs and passkeys) of a deleted client stay in the table until
`07-prune-factors.sh` removes them. Both pruning scripts run by hand under the admin profile
(`ADMIN_PROFILE=default`), and neither runs in CI.

- `06-prune-clients.sh` lists every client through the admin API and proposes the test clients
  that piled up, by name: `smoke`, `bruno boundary A`, `B` and `C`, `bruno demo`,
  `bruno demo renamed`, `bruno rate limit`, `bruno portal` and `rate-limit-probe`. It matches by name across
  every owner, including a Portal tenant's, so check the listing before answering. `--keep <id>`
  drops one from the proposal and `--also <id>` adds one. It never offers the Console's own
  client (output `AuthGateClientId` of `auth-gate-console-<stage>`) nor the Portal's
  (`auth-gate-portal-<stage>`), and it refuses to run if those ids do not
  read cleanly. It deletes only after you type `delete <n>`. The admin key goes to curl on stdin and
  is never printed.
- `07-prune-factors.sh` counts the factor items of soft-deleted clients; with `--apply` it
  deletes them. It needs `Scan`, which no Lambda role or the deployer has. Safe to rerun.

The smokes now delete the clients they create, so CI no longer adds to the pile.

A rotate that loses a race to a delete can leave an orphan `K#` key item that points at a deleted
client. `Authenticate` refuses it, so it does no harm.

## Rate limit

The `verify` role is limited per client with a token bucket stored in the table. A new
client holds **100 tokens (burst) and refills 50 per second**. A call that finds the
bucket empty gets **429** with the body `{"error":"rate_limited"}` and a `Retry-After`
header in whole seconds. The `admin` role is not limited. Change one client's bucket
with the admin key, no deploy needed (`perSecond` must be above 0):

```bash
curl -X PATCH "$BASE/api/clients/$CLIENT_ID" \
  -H "x-internal-access-key: $ADMIN_KEY" -H 'content-type: application/json' \
  -d '{"rateLimit": {"burst": 200, "perSecond": 100}}'
```

Each call spends its token with one conditional write, so simultaneous callers on a full
bucket are served up to the burst instead of taking turns. If DynamoDB throttles the
limiter's own calls, the request is refused with the same **429** and `Retry-After: 1`,
because a client flooding its own bucket is the one that gets throttled. If the table fails
in any other way the limiter lets the request through
([memo](docs/memo/rate-limit-bucket.md)).

## The WebAuthn relay

The browser runs `navigator.credentials.create/get`, but only against the client's own
backend, which relays to auth-gate
([memo](docs/memo/webauthn-without-a-browser.md)):

```text
browser ──► client backend ──► POST /api/webauthn/registrations ──► options
browser ◄── client backend ◄──────────────────────────────────────┘
browser (navigator.credentials.create) ──► attestation ──► client backend
                        client backend ──► POST /api/webauthn/registrations/{id} ──► credentialId
```

Verification is the same shape: `POST /api/challenges`, then
`POST /api/challenges/{id}/verify`. A PIN uses the same two calls
([memo](docs/memo/pin-goes-through-a-challenge.md)). Challenges are single use, live five
minutes and are scoped to the client ([memo](docs/memo/challenge-is-client-scoped.md)).

## How a consumer verifies a token

A successful verify returns `{token, expiresAt, factor}`. The token is an ES256 JWT.

1. Fetch `<base URL>/.well-known/jwks.json` (cacheable for 300 s) and pick the key whose
   `kid` matches the token header. Verify the signature.
2. Check `iss` is your auth-gate base URL (`https://<host>`), `exp` has not passed, and
   `aud` is your own `clientId`.
3. Check `purpose` equals the action you are about to allow. A token issued for
   `transfer` does not authorise `change-email`. `sub` is your `subjectId`, `amr` is
   `["pin"]` or `["webauthn"]`, and `jti` is the challenge id.

Tokens last `tokenTtlSec` (client default 300, max 900). KMS signs them, not a key in
SSM ([memo](docs/memo/kms-signs-not-ssm.md)).

## Rotating the signing key

Order matters, because consumers cache the JWKS for 300 s:

1. Create the new KMS key.
2. Add its id to `KMS_KEY_IDS` on the `jwks` function, so it is published first.
3. Switch `KMS_KEY_ID` on the `verify` function to the new key.
4. After 15 minutes (the longest token lifetime) remove the old id from `KMS_KEY_IDS`.

## The Console

The Console is the operator's screen for client work: register and manage clients, rotate
or revoke keys, disable a client, clear one user's factors, and read the audit log. It is a
separate app with its own CloudFront URL and its own stack, `auth-gate-console-<stage>`, so a
Console deploy cannot touch the service and the service gains no browser-facing path. The
browser never holds the admin key; the Console backend reads it from SSM. Design:
[`docs/superpowers/specs/2026-11-06-console-design.md`](docs/superpowers/specs/2026-11-06-console-design.md).

```text
browser ──► CloudFront (Console URL)
              ├─ /api/*  → Lambda Function URL (console, Go, arm64; x-origin-verify)
              └─ /*      → S3 (SPA; CloudFront Function rewrites routes to /index.html)
console ──► DynamoDB auth-gate-console-<stage>   (admins, sessions, used tokens, audit)
console ──► AuthGate /api/clients*               (admin key, from SSM)
console ──► AuthGate /api/challenges, /factors…  (its own client key, from SSM)
console ──► AuthGate /.well-known/jwks.json      (verifies tokens, cached 300 s)
```

`cmd/console` does not import `internal/auth/service`; `make check` fails if it does. That
keeps the KMS and AuthGate table permissions out of a browser-facing Lambda.

### Sign-in model

- Accounts are created by an operator or a `master`; there is no public sign-up. Roles:
  `admin` manages clients and reads the audit log, `master` also manages admin accounts.
- Sign-in is a password, then a PIN or passkey checked by auth-gate. Unknown user and wrong
  password answer the same 401. Five failures lock the account for 15 minutes.
- The first sign-in enrolls: the temporary password is changed and a first factor added
  before any other screen opens.
- Sessions are server-side (cookie `__Host-console_session`, HttpOnly, SameSite=Strict), 30
  minutes idle and 8 hours absolute. Resetting, clearing or disabling an admin ends that
  admin's sessions at once, because every request compares the session's epoch with the
  account's ([memo](docs/memo/console-sessions.md)).
- Step-up is asked in stage `full`, never in `enroll`. An enrolling session that never proved a
  factor (`FactorProved`) cannot write or reach a full session once the account has a factor:
  the write is refused and the session ends. First-factor enrollment is serialised by a
  25 second lease on the account, and a session that loses answers `409
  enrollment_in_progress`. AuthGate does not fence writes, so this is bounded by timeouts,
  not absolute: a sent write lands within AuthGate's 10 second function timeout, and the
  Console sends the first-factor write only up to 13 seconds after the claim (the lease less
  those 10 seconds and a 2 second margin), so it lands inside the lease. The Console Lambda's
  timeout bounds when the write is sent, not when it lands, so it is not what keeps the bound.
- Sensitive actions (rotate or revoke keys, disable a client, clear a user's factors, change
  a client's origins, PIN policy, token TTL or rate limit, change a password, add or remove
  your own factor, anything on another admin's account) ask for a PIN or passkey again. The
  token is single use and bound to the admin, the action and the target, so an approval to
  rotate client A does not rotate client B. It is not bound to the request body: an approved
  update of a client can still change any field of that client, once.
- The Console's own client is read-only through the Console (`409 own_client_protected` on
  patch, rotate, revoke previous keys, disable and purge). Emergency changes to it use the scripts.
  Only the Console UI and `06-prune-clients.sh` protect it: an admin-key `DELETE /api/clients/{id}`
  sent directly (curl, Bruno) soft-deletes it, and there is no undelete. Recover with
  `scripts/console/register-client.sh` and a Console redeploy with the new client id.
- `/api/auth/*` is limited per IP, read from `CloudFront-Viewer-Address`: a burst of 10, then
  one request per 6 seconds.

### Audit

Every write records an audit item, success or failure, with its source (`web` or `cli`), the
actor, the target, the result and a small allow-listed detail (for example the fields a patch
changed, or why a sign-in failed). A target is stored only once it passed validation, and
client values only after auth-gate accepted them. No key, PIN, password or token is stored.
Two kinds of event go to the log, not the table, because they are unbounded: limiter refusals
and failed writes that have no session (other than under `/api/auth`, which is stored with
empty identity). Audit items have no TTL.

### Console API

All under `/api`, JSON. Errors are `{"error": "<snake_case>"}`.

| Route | Session | Notes |
| --- | --- | --- |
| `POST /auth/login`, `/auth/second-factor/start`, `/auth/second-factor/verify`, `/auth/logout` | by stage | |
| `GET /me`, `POST /me/password`, `/me/pin`, `/me/passkeys/start`, `/me/passkeys/finish`, `DELETE /me/pin`, `/me/passkeys/{id}` | any stage for `GET /me`; enroll or full for the rest | Writes need step-up in `full` (a passkey at its start), never in `enroll`. The last factor cannot be removed. |
| `POST /stepup/start`, `/stepup/verify` | full | `start` takes the action and its `target`; `400 invalid_target` otherwise. |
| `GET /clients`, `GET /clients/{id}`, `POST /clients` | full | The key is returned once. |
| `PATCH /clients/{id}` | full | Step-up unless only `name` changes. |
| `POST /clients/{id}/keys`, `DELETE /clients/{id}/keys/previous`, `POST /clients/{id}/disable`, `DELETE /clients/{id}/subjects/{subjectId}` | full | Step-up. |
| `POST /clients/{id}/enable` | full | |
| `GET /audit` | full | Newest first, by month. |
| `GET /admins` | master | |
| `POST /admins`, `POST /admins/{username}/reset-password`, `/clear-factors`, `/disable`, `/enable`, `/role` | master | Step-up. You cannot disable, demote or clear yourself, and the last active master cannot be disabled or demoted. |

### Interface

A left sidebar that collapses to an icon rail, with the state kept in the browser. The avatar
menu in the header opens My account and Sign out. Preferences on My account holds the
language (English or Vietnamese, English by default) and the appearance (light, dark or
system, system by default).

### Run it locally

Needs Docker, Go, Node.js and pnpm as in [Local development](#local-development).

```bash
pnpm install
make dev                                        # AuthGate on :8080
scripts/console/register-client.sh local        # writes .env.console (gitignored)
make console-dev                                # the Console API on :8788
STAGE=local scripts/console/create-admin.sh --username <name>   # first master
pnpm --filter console-web dev                   # the web app on :5173
```

`create-admin.sh` asks for the password on the terminal and passes it on stdin, never on
argv. The Console client's origin is `http://localhost:5173`, so passkeys need that port.
The Bruno collection for the API is in `bruno-console/`; see its docs for how to run it.

### Deploy

Scripts are in `scripts/console/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION`. AuthGate must already be deployed in that stage; if
the [permissions boundary](#permissions-boundary) is new, follow the order there first.

1. `deploy.sh`, first pass, under the admin profile ([First deploy of a
   stage](#first-deploy-of-a-stage)): writes the `origin-verify` parameter if absent, builds the
   Lambda and the web app, deploys the stack, uploads the site and invalidates the cache. With no
   AuthGate client id yet `/api` is down and it says so.
2. `register-client.sh stg`: register the Console as an AuthGate client with the stack's URL
   as its `rpId` and origin. It writes the client key to SSM and prints the client id; it
   refuses to overwrite a key that is already stored.
3. `AUTHGATE_CLIENT_ID=<printed id> deploy.sh`, second pass. This is the same two-pass shape as
   AuthGate's own first deploy.
4. `ADMIN_PROFILE=default create-admin.sh --username <name>`: the first master, written to the
   Console table under an operator profile, not the deployer's.
5. `smoke.sh`: a deep link serves HTML with a CSP that has no `unsafe-inline`, `/api/nope` is a
   JSON 404, the raw Function URL answers 401, then Bruno `01-boundary`. It needs no
   credential.
6. `purge.sh`: remove the stack, after a typed confirmation. The Console table has deletion
   protection (it holds the accounts and the audit trail), so a plain `delete-stack` fails;
   `purge.sh` turns it off just before the delete. It never touches AuthGate's `admin-key`.

The account's Lambda concurrency limit is 40. Ask for a quota increase in Service Quotas
before production, then deploy with `CONSOLE_RESERVED_CONCURRENCY=5`, so a login flood (each
sign-in runs a 64 MiB argon2id hash) cannot starve AuthGate's functions.

### The admin key and the Console

The Console reads the admin key at cold start. Rotating `admin-key` therefore means
redeploying both stacks, `auth-gate-<stage>` and `auth-gate-console-<stage>`.

### If AuthGate is down

Nobody can sign in to the Console, because the second factor is checked by AuthGate. The
emergency path skips the Console: call the [Admin API](#admin-api) directly with the admin
key from SSM, as in the rate-limit example above.

### Recovering a locked-out master

There is no email, so there is no reset link. An operator with AWS access to the Console table
runs, under `ADMIN_PROFILE` (default `default`):

```bash
scripts/console/admin.sh reset-password --username <name>   # a new temporary password
scripts/console/admin.sh clear-factors  --username <name>   # enroll again at next sign-in
```

`disable --username <name>` and `create --username <name> --role master|admin` also exist. A
reset also clears a lockout, and ends the account's sessions. Each command reads the password
from stdin, never argv, and writes an audit item with source `cli` and the actor `cli:<operating system user>`.
`clear-factors` calls AuthGate with the admin key, read from SSM by the operator's profile.

### What a stolen deployer key can do

The permissions boundary caps every `auth-gate-*` IAM role the deployer creates, so a stolen
deployer key cannot mint a role more powerful than the boundary. Its KMS, CloudFront, S3 and log
rights reach only auth-gate's own resources ([Deployer scope](#deployer-scope)). It can still
change Lambda code and the Console's site (so it can swap the JavaScript the admin loads), read
`admin-key` and `portal-key` from SSM, edit any CloudFront cache, origin request, response headers or origin
access control policy in the account (CloudFront cannot scope those), and it holds wide rights on
Route 53 and ACM until the custom domain work settles them. CI does not use the key: it assumes the
role `ci-auth-gate-deploy` ([Continuous deploy](#continuous-deploy)). Keep the key out of shells
you do not control.

## The Portal

The Portal is the self-service screen: anyone signs up, enrols a second factor, and manages
their own AuthGate clients in a workspace. It is a third app with its own stack,
`auth-gate-portal-<stage>`, its own CloudFront URL and its own table, built like the Console and
reaching AuthGate only over HTTP. Design:
[`docs/superpowers/specs/2026-11-22-self-service-design.md`](docs/superpowers/specs/2026-11-22-self-service-design.md);
the reasons for the account rules are in [portal-accounts](docs/memo/portal-accounts.md) and
[client-ownership](docs/memo/client-ownership.md).

```text
browser ──► CloudFront (Portal URL)
              ├─ /api/*      → Lambda Function URL (portal, Go, arm64; x-origin-verify)
              ├─ /avatars/*  → S3 avatar bucket (origin access control)
              └─ /*          → S3 (SPA; CloudFront Function rewrites routes to /index.html)
browser ──► S3 (presigned POST to pending/, an avatar upload; confirmed into avatars/)
portal  ──► DynamoDB auth-gate-portal-<stage>  (users, workspaces, sessions, tokens, limiter, audit)
portal  ──► AuthGate /api/challenges, /factors…   (its own client key: the second factor of its users)
portal  ──► AuthGate /api/portal/*                (the portal key and x-tenant-id: a workspace's clients)
portal  ──► SES (mail), Cloudflare Turnstile, Have I Been Pwned (range API)
```

`cmd/portal` imports neither `internal/auth/service` nor `internal/console`, and it never holds the
admin key; `make check` fails if either import appears. It shares `internal/platform`
(`websession`, `lockout`, `lease`, `firstfactor`, `auditlog`, `password`, `webapi`) and
`packages/web-kit` with the Console.

### Accounts

- Sign-up takes an email, a display name, a password (12 to 128 bytes, checked against HIBP's range
  API, which fails open) and a Turnstile token. Sign-up, resend, forgotten password and
  factor-reset request always answer `202 {"status":"check_email"}`, whether or not the address has
  an account, and take at least 1500 ms. A sign-in failure is always `401 invalid_credentials`.
- The email link, with the account's password, verifies the address (24 hours), creates the
  personal workspace and starts a session in stage `enroll`. A wrong password answers like a bad
  link, counts as a failed sign-in and leaves the link usable. A new sign-up for an address whose
  account is still unverified replaces that account (password, name, language) and voids its older
  links, so whoever signed up first with someone else's address does not keep it. An unverified
  account expires after 7 days.
- A second factor, a PIN or a passkey checked by AuthGate, is mandatory. Ten recovery codes
  (`XXXXX-XXXXX`) are shown once with the first factor. A recovery code signs in to `enroll`, so the
  person adds a new factor; it does not open `full`.
- A lost factor without a code: an emailed link (1 hour), then a 24-hour wait that the next sign-in
  with the old factor cancels. It is applied at the first sign-in after the wait.
- Reset and factor-reset links are bound to the account's session epoch and to the address they
  were sent to: a password reset, a password change or an applied factor reset voids every link
  already sent, and an email change voids those sent to the old address and cancels a pending
  factor reset.
- Sessions are the Console's model: cookie `__Host-portal_session`, server-side, stages `half` (10
  minutes), `enroll` (30 minutes) and `full` (30 minutes idle, 8 hours absolute). Five failed
  sign-ins lock the account for 15 minutes; the count resets only on a completed sign-in.
- Turnstile is always on for the four email routes, and on sign-in after 2 failures in 15 minutes
  for the IP or the account (`403 turnstile_required`).
- Limits (`429 rate_limited` with `Retry-After`): sign-up 5 per IP then one per 10 minutes, 3 emails
  per address per hour; sign-in 10 per IP then one per 6 seconds, in separate buckets for the
  password, the second factor and recovery codes; forgotten password and resend 10
  per IP per hour; a signed-in user, at any stage (`half`, `enroll` or `full`), 60 requests then one
  per second. Fixed cost: none, and no WAF.
- Step-up (a PIN or passkey, single use, bound to the action and its target) guards `rotate-key`,
  `revoke-previous-keys`, `disable-client`, `purge-subject`, `delete-client`, `change-password`,
  `change-email`, `add-factor`, `remove-factor`, `regenerate-recovery-codes` and `delete-account`.
- A workspace is a tenant at AuthGate. The personal workspace is its owner's only, and a user who is
  not a member of a workspace gets `404` on its routes, the same as an unknown one, before AuthGate
  is called. Roles: everyone reads; owner, admin and member create, edit, rotate and revoke; owner
  and admin disable, enable, purge and delete.

### Portal API

All under `/api`, JSON, errors `{"error": "<snake_case>"}`.

| Route | Session | Notes |
| --- | --- | --- |
| `GET /config` | none | The Turnstile site key. |
| `POST /auth/signup`, `/auth/resend-verification`, `/auth/forgot`, `/auth/factor-reset/request` | none | Turnstile; always `202 check_email`. |
| `POST /auth/verify-email`, `/auth/reset-password`, `/auth/factor-reset/confirm`, `/auth/confirm-email` | none | A single-use token from an email link. `verify-email` also takes the account's `password`. |
| `POST /auth/login`, `/auth/second-factor/start`, `/auth/second-factor/verify`, `/auth/recovery`, `/auth/logout` | by stage | |
| `GET /me`, `POST /me/pin`, `/me/passkeys/start`, `/me/passkeys/finish`, `/me/recovery-codes`, `DELETE /me/pin`, `/me/passkeys/{id}` | any stage for `GET /me`; enroll or full for the rest | Writes need step-up in `full`, never in `enroll`. The last factor cannot be removed. |
| `POST /me/display-name`, `/me/password`, `/me/email`, `/me/avatar`, `/me/avatar/confirm`, `DELETE /me/avatar`, `DELETE /me` | full | Password, email and account deletion need step-up. |
| `POST /stepup/start`, `/stepup/verify` | full | `start` takes the action and its `target`. |
| `GET /tenants` | full | The caller's workspaces, personal first. |
| `GET /t/{tenantId}/clients`, `/t/{tenantId}/clients/{id}`, `POST /t/{tenantId}/clients`, `PATCH /t/{tenantId}/clients/{id}` | full | The key is returned once. `rateLimit` is not accepted. |
| `POST .../clients/{id}/keys`, `DELETE .../clients/{id}/keys/previous`, `POST .../clients/{id}/disable`, `DELETE .../clients/{id}/subjects/{subjectId}`, `DELETE .../clients/{id}` | full | Step-up. `409 disabled_by_operator` for a client an operator disabled. |
| `POST .../clients/{id}/enable` | full | |
| `GET /t/{tenantId}/audit` | full | Owner and admin; newest first. |

### Run it locally

Needs Docker, Go, Node.js and pnpm as in [Local development](#local-development).

```bash
pnpm install
make dev                                        # AuthGate on :8080
scripts/portal/register-client.sh local         # writes .env.portal (gitignored)
make portal-dev                                 # the Portal API on :8789
pnpm --filter portal-web dev                    # the web app on :5174
```

Locally, mail is written as files in `.portal-mailbox/` (open the newest to find the link),
avatars go to `.portal-avatars/`, and Turnstile uses Cloudflare's public test keys, which only work
against the real siteverify endpoint (so a sign-up makes one outbound call to Cloudflare and one
to HIBP). The Portal client's origin is `http://localhost:5174`, so passkeys need that port. The
Bruno collection for the API is in `bruno-portal/`; `01-boundary` needs no credential:

```bash
cd bruno-portal/environments && cp local.example.yml local.yml
cd .. && ../node_modules/.bin/bru run 01-boundary --env local -r
```

### Deploy

Scripts are in `scripts/portal/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION`. AuthGate must already be deployed in that stage with the
`portal-key` parameter, and the [permissions boundary](#permissions-boundary) and the deployer
policies must carry the Portal's rights first ([Rollout of P2](#rollout-of-p2-the-portal)).

1. `deploy.sh`, first pass, under the admin profile ([First deploy of a stage](#first-deploy-of-a-stage)).
   It needs `EMAIL_FROM` (the SES identity address) and `TURNSTILE_SITE_KEY` (public) in the
   environment. It writes `origin-verify` if absent, and `turnstile-secret` once, from the
   environment variable `TURNSTILE_SECRET` through a 0600 file, never argv, and unsets it before
   anything is built; later runs keep it.
   It builds the Lambda and the web app, deploys the stack and uploads the site. With no AuthGate
   client id yet `/api` answers `502` and it says so.
2. `register-client.sh stg`: register the Portal as an AuthGate client with the stack's URL as its
   `rpId` and origin; it writes the client key to SSM and prints the client id, and refuses to
   overwrite a key that is already stored.
3. `AUTHGATE_CLIENT_ID=<printed id> deploy.sh`, second pass. It also sets `PublicBaseUrl` (the
   links in emails and the avatar upload origin) from the stack's own URL.
4. `smoke.sh`: a deep link serves HTML with a CSP that has no `unsafe-inline` and names Turnstile,
   `/api/nope` is a JSON 404, the raw Function URL answers 401, `/api/config` carries a site key,
   then Bruno `01-boundary`. It needs no credential and can run any number of times.
5. `purge.sh`: remove the stack after a typed confirmation. The table holds every account and the
   audit trail and has deletion protection, so `purge.sh` turns it off just before the delete. It
   never touches `/credo/auth-gate/<stage>/portal-key`, which belongs to AuthGate.

CI runs `deploy.sh` and `smoke.sh` after the Console's ([Continuous deploy](#continuous-deploy)),
with `EMAIL_FROM` and `TURNSTILE_SITE_KEY` from the repository variables `PORTAL_EMAIL_FROM` and
`PORTAL_TURNSTILE_SITE_KEY`. They are variables, not secrets: the sender address and the site key
are both public. The Lambda has no reserved concurrency (the account quota is small), so the
per-IP and per-account limiters are the guard. While SES is in its sandbox it sends only to
verified addresses.

### Known limits

- A deleted avatar can stay in CloudFront's cache for up to 24 hours.
- A tenant can register any `rpId` and origins for its own client: there is no domain-ownership
  check. The browser still binds a passkey to the origin it was made on.
- Anyone can force a Turnstile challenge on a given address by failing a sign-in for it twice; that
  is friction, not access.
- The four email routes always take about 1.5 seconds.

### Rollout of P2 (the Portal)

This branch adds the Portal and widens the permissions boundary and the deployer policy. It needs a
person at an admin profile, in this order, from the `feat-self-service` worktree, before the merge.
Order matters: until step 3 is done the first deploy cannot succeed, and until step 4 is done CI's
preflight refuses to deploy (the Portal stack and its client id must exist), so the hand deploy
comes first and the merge second. Nothing in the blocks needs editing: each one asks for what it
needs, and no secret goes on a command line.

**1. SES.** The Lambda may send only from `EMAIL_FROM`, so the address typed here must be the one
given to the deploy. In the sandbox SES sends only to verified addresses, so use the address you
will sign up with in step 7. The second command makes SES stop mailing addresses that bounced or
complained.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1
read -r -p 'Sender address: ' addr
aws sesv2 create-email-identity --email-address "$addr"
aws sesv2 put-account-suppression-attributes --suppressed-reasons BOUNCE COMPLAINT
)
```

SES mails a verification link to that address. Open it before going on; the proofs below show
`true` for `VerifiedForSendingStatus` once it has been clicked.

**2. Turnstile.** In the Cloudflare dashboard, Turnstile, add a widget named `auth-gate-portal-stg`,
mode Managed, with the hostname `localhost` only: the CloudFront host does not exist yet. Copy the
site key (public) and the secret. Step 4 reads the secret from the terminal without echo.

**3. The boundary and the deployer policy**, back to back, in this order. `00-deployer.sh` rewrites
the policies the CI role also holds, and `00-ci-role.sh` re-asserts the role, so run them without
a merge or a deploy in between. Then wait a minute: IAM changes take a moment to apply.

```bash
ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
sleep 60
```

**4. The first Portal deploy**, under the admin profile, two passes with the registration between.
The block stops at the first failing command. On a rerun press Enter at the secret prompt (the
secret is already in SSM and is kept), and skip `register-client.sh` once the client key exists: it
refuses to overwrite it, so run the second pass alone with `AUTHGATE_CLIENT_ID` set.

```bash
cd /home/work/auth-gate/feat-self-service && pnpm install --frozen-lockfile && (
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
read -r -p 'Sender address (the SES identity): ' EMAIL_FROM
read -r -p 'Turnstile site key: ' TURNSTILE_SITE_KEY
read -rs -p 'Turnstile secret (not echoed): ' TURNSTILE_SECRET && echo
export EMAIL_FROM TURNSTILE_SITE_KEY
# The secret goes to pass 1 alone, in its environment, never exported; deploy.sh drops it once
# it is in SSM, before anything is built.
TURNSTILE_SECRET="$TURNSTILE_SECRET" scripts/portal/deploy.sh   # pass 1: /api answers 502 until pass 2
unset TURNSTILE_SECRET
scripts/portal/register-client.sh stg         # prints AUTHGATE_CLIENT_ID=<id>
read -r -p 'AUTHGATE_CLIENT_ID printed above: ' AUTHGATE_CLIENT_ID
export AUTHGATE_CLIENT_ID
scripts/portal/deploy.sh                      # pass 2: sets the client id and PublicBaseUrl
)
```

Now the CloudFront host exists. Print it, add that hostname (without `https://`) to the Turnstile
widget in the dashboard, next to `localhost`, then smoke it:

```bash
AWS_PROFILE=default aws cloudformation describe-stacks --region ap-southeast-1 \
  --stack-name auth-gate-portal-stg \
  --query 'Stacks[0].Outputs[?OutputKey==`BaseUrl`].OutputValue | [0]' --output text
AWS_PROFILE=default scripts/portal/smoke.sh
```

**5. The two repository variables.** In GitHub, the repository, Settings, Secrets and variables,
Actions, the Variables tab, New repository variable: `PORTAL_EMAIL_FROM` is the sender address of
step 1 and `PORTAL_TURNSTILE_SITE_KEY` is the site key of step 2. Without them the merge's Portal
deploy stops at "EMAIL_FROM is required", after AuthGate and the Console have deployed.

**6. Push the branch, open the pull request and merge it.** CI runs `check`, then deploys AuthGate,
the Console and the Portal and runs the three smokes. The preflight passes only because steps 3 and
4 are done. If a smoke fails after a successful deploy, the new code is live: fix forward.

**7. The browser run**, on the Portal's CloudFront URL (step 4 printed it):

1. Sign up with the SES-verified address. With any other address SES refuses to send in the
   sandbox, the Portal logs it and still answers `check_email`, and no mail arrives.
2. Open the verification link; you land in `enroll`.
3. Add a PIN or a passkey; save the ten recovery codes (copy or download) and tick the box.
4. Create a client in the personal workspace; copy its key, then rotate the key and read the new one.
5. Sign out, then sign in with the password and a recovery code instead of the factor: you land in
   `enroll`; add a factor again.
6. Sign out, choose "forgot password", open the link, set a new password and sign in with it.
7. Optional: delete the client and the account from My account.

**The proofs**, read-only, under the admin profile in a subshell, so it does not leak into later
deploys. Expect: the SES identity `true` and the sandbox still on (`ProductionAccessEnabled`
`false`) with suppression `BOUNCE` and `COMPLAINT`; the boundary statements `SendPortalEmail` and
`PortalAvatars`; both managed policies on the user and on the role; the Portal's three parameters
(names and types only: `authgate-client-key` SecureString, `origin-verify` String,
`turnstile-secret` SecureString); the stack complete, with a client id and the sender address;
deletion protection `true`; only a site key in `/api/config`; `401` for the raw URL and `401` for a
tenant route without a cookie. `gh variable list` shows the two variables.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
read -r -p 'Sender address: ' addr
acct="$(aws sts get-caller-identity --query Account --output text)"
aws sesv2 get-email-identity --email-address "$addr" --query VerifiedForSendingStatus
aws sesv2 get-account --query '[ProductionAccessEnabled,SuppressionAttributes.SuppressedReasons]'
b="arn:aws:iam::$acct:policy/auth-gate-boundary"
v="$(aws iam get-policy --policy-arn "$b" --query Policy.DefaultVersionId --output text)"
aws iam get-policy-version --policy-arn "$b" --version-id "$v" --query 'PolicyVersion.Document.Statement[].Sid'
aws iam list-attached-user-policies --user-name auth-gate-deployer --query 'AttachedPolicies[].PolicyName'
aws iam list-attached-role-policies --role-name ci-auth-gate-deploy --query 'AttachedPolicies[].PolicyName'
aws ssm describe-parameters \
  --parameter-filters "Key=Name,Option=BeginsWith,Values=/credo/auth-gate-portal/$STAGE/" \
  --query 'Parameters[].[Name,Type]' --output text
out() { aws cloudformation describe-stacks --stack-name "auth-gate-portal-$STAGE" \
  --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue | [0]" --output text; }
aws cloudformation describe-stacks --stack-name "auth-gate-portal-$STAGE" --query 'Stacks[0].StackStatus' --output text
echo "AuthGateClientId: $(out AuthGateClientId)"
fn="$(aws cloudformation describe-stack-resource --stack-name "auth-gate-portal-$STAGE" \
  --logical-resource-id Portal --query StackResourceDetail.PhysicalResourceId --output text)"
aws lambda get-function-configuration --function-name "$fn" --query 'Environment.Variables.EMAIL_FROM' --output text
aws dynamodb describe-table --table-name "auth-gate-portal-$STAGE" --query Table.DeletionProtectionEnabled
base="$(out BaseUrl)"
raw="$(out RawPortalUrl)"
curl -sS "${base%/}/api/config" | jq 'keys'
curl -sS -o /dev/null -w '%{http_code}\n' "${raw%/}/api/nope"
curl -sS -o /dev/null -w '%{http_code}\n' "${base%/}/api/t/01JD0000000000000000000000/clients"
)
gh variable list
```

## Local development

You need Go (`go 1.26.0` per `go.mod`), Docker, Node.js 22 or newer and pnpm. Put
golangci-lint v2 on your `PATH` (`$(go env GOPATH)/bin`).

```bash
make dev    # DynamoDB Local on :8000 (docker compose) and all roles on :8080
```

`make dev` serves the three roles on one port under the prefixes `/verify`, `/admin` and
`/jwks`, with an in-memory signer and the admin key `dev-admin`. The Bruno collection
adds the prefix itself when `baseUrl` is a localhost URL. Run it from `bruno/`:

```bash
cd bruno/environments && cp local.example.yml local.yml
cd ../.. && pnpm install   # once: installs the pinned Bruno CLI
cd bruno && ../node_modules/.bin/bru run --env local --sandbox=developer
```

`make check` runs `go vet`, `golangci-lint run` and `go test ./...`; `make test-store`
also runs the store tests against DynamoDB Local.

## Deploy

The deploy and smoke steps also run in CI on every merge to `main`
([Continuous deploy](#continuous-deploy)). A person runs them for first deploys, recoveries and
other stages, with AWS access. They live in `scripts/setup-and-deploy/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION` (default `ap-southeast-1`):

0. `00-deployer.sh` (once per account and after changing either JSON, with an admin profile:
   `ADMIN_PROFILE=default`): create or update the permissions boundary `auth-gate-boundary` from
   `infra/role-boundary.json`, then the IAM user `auth-gate-deployer` with
   the two managed policies, scoped to `auth-gate-*` resources, and a local AWS profile of
   the same name. The deployer's rights are split over two files because one managed policy
   holds at most 6144 characters: `auth-gate-deployer-policy` from `infra/deployer-policy.json`
   (CloudFormation, S3, Lambda, the IAM roles under the boundary, DynamoDB, KMS, SSM and logs)
   and `auth-gate-deployer-edge-policy` from `infra/deployer-policy-edge.json` (CloudFront
   distributions and policies, edge functions, the optional ACM and Route 53 rights). Both are
   attached to the user and to the CI role, and together they are the deployer's rights. The deployer cannot create the stacks: a stage's first deploy runs under the
   admin profile ([First deploy of a stage](#first-deploy-of-a-stage)).
1. `00-cert.sh DOMAIN` (optional): request an ACM certificate in `us-east-1`.
2. `01-ssm.sh`: write the random `admin-key` and `portal-key` (SecureStrings) and
   `origin-verify` (plain String; CloudFront cannot resolve a SecureString). A parameter that
   exists is kept, and no value ever goes on an argv.
3. `02-build.sh`: cross-compile `bin/bootstrap.zip` for linux/arm64.
4. `03-deploy.sh`: deploy the SAM stack `auth-gate-<stage>` and print the one `BaseUrl`. The
   first deploy of a stage runs under the admin profile; it will fail under the deployer
   ([First deploy of a stage](#first-deploy-of-a-stage)).
   Without a custom domain the first deploy runs twice: it reads the distribution domain,
   then redeploys to set `BASE_URL`, which avoids a CloudFormation dependency cycle.
5. `04-smoke.sh`: run the Bruno collection, then `cmd/smoke`, a real WebAuthn round trip.
6. (once, after the first deploy that has client listing) `05-backfill-clients.sh`
   (`ADMIN_PROFILE=default`): write the list-index item for clients created before it
   existed. It needs `Scan`, which no Lambda role or the deployer has, so it runs under the
   operator profile. Safe to rerun; prints counts only.
7. `99-purge.sh`: tear the stage down, after a typed confirmation. AuthGate's table has
   deletion protection (it holds every client and every user's factors), so a plain
   `delete-stack` fails; `99-purge.sh` turns it off just before the delete. Rebuilding the
   stage afterwards is a first deploy, under the admin profile.

Prerequisites: AWS CLI v2 and SAM CLI. A new AWS account may refuse the distribution with
"Your account must be verified before you can add new CloudFront resources"; only AWS Support
can lift that (open an Account and billing case), so do it before the first deploy.

If a deploy fails, CloudFormation rolls back and leaves the stack in `ROLLBACK_COMPLETE`
(or `ROLLBACK_FAILED`), which cannot be updated: delete it with
`aws cloudformation delete-stack --stack-name auth-gate-<stage>` before deploying again. A
missing deployer permission goes into the file whose statements it belongs with:
`infra/deployer-policy-edge.json` for CloudFront, edge functions and the custom domain,
`infra/deployer-policy.json` for everything else; rerun `00-deployer.sh`.
Both tables have deletion protection, which blocks a plain `delete-stack`: to clear a failed
stack, run `99-purge.sh` (AuthGate) or `scripts/console/purge.sh` (the Console), which turn it
off after their typed confirmation.

### Permissions boundary

`auth-gate-boundary` is a ceiling on what any `auth-gate-*` role can do, whatever is attached
to it. The deployer can create or change such a role only if the role carries that boundary,
so a right added to a Lambda role must also go into `infra/role-boundary.json`, followed by
`00-deployer.sh`, or the function is denied it at run time. If the boundary turns out too small,
edit that JSON and rerun `00-deployer.sh`: the new default version applies without CloudFormation.

When the boundary first reaches a stage whose stack already exists, one AuthGate deploy runs
under an admin profile. The deployer may change only roles that already carry the boundary, and
AuthGate's do not yet. If CloudFormation calls `PutRolePolicy` or `AttachRolePolicy` before it
sets the boundary, the deployer is refused; and if that update fails after any role has the
boundary, rollback must remove it, which the deployer may not do. So run, in this order:

1. `00-deployer.sh`, then wait one minute: IAM changes take a moment to apply everywhere.
2. `02-build.sh`, then `AWS_PROFILE=<admin> scripts/setup-and-deploy/03-deploy.sh`, from a tree
   whose template has the boundary.
3. `scripts/console/deploy.sh`, under the deployer. So is every later deploy.

If a deployer-run update stops at `UPDATE_ROLLBACK_FAILED` because it could not remove a
boundary, finish it with an admin profile, then deploy again:
`aws cloudformation continue-update-rollback --profile <admin> --region <region> --stack-name auth-gate-<stage>`.
Tearing down a stack whose roles never got the boundary needs an admin profile too.

### Deployer scope

The deployer's S3, KMS, CloudFront distribution and log rights reach only auth-gate's own
resources:

- **S3:** both deploys upload to their own prefix of the SAM bucket the account shares,
  `aws-sam-cli-managed-default-samclisourcebucket-m5yrn5zzyiu6` (`--s3-prefix
  auth-gate-<stage>`, `auth-gate-console-<stage>`). The deployer may read and write objects
  only under `auth-gate-*` in that bucket, and list only that prefix. Artifacts of deploys from
  before the prefix stay at the bucket root; the lifecycle rules of [Continuous deploy](#continuous-deploy) expire the new ones.
- **KMS:** every right on a key requires the tag `app = auth-gate`, including `PutKeyPolicy`,
  `TagResource` and `DescribeKey`, so tagging cannot pull another project's key into scope.
  `CreateKey` requires the request to carry that tag.
- **CloudFront:** both distributions carry `app = auth-gate`. Reading, changing, deleting,
  tagging and invalidating a distribution requires that tag; creating one requires it in the
  request. Cache, origin request, response headers and origin access control policies stay
  account-wide: CloudFront has no tag or name condition for them.
- **Logs:** reading log events only from `/aws/lambda/auth-gate-*`.

The tags, the prefix and AuthGate's table protection must be in AWS before the narrowed policy
is. Once `00-deployer.sh` applies it, a deploy from a tree without `--s3-prefix` fails at the
upload, so the first rollout runs from `main`, and every other worktree rebases on `main` before
it deploys again:

1. Merge this change to `main`. From `main`, with the old policy still applied: `02-build.sh`,
   `03-deploy.sh`, then `scripts/console/deploy.sh`. The old policy already allows the tags, the
   new prefix and the table update. If this step fails, CloudFormation rolls the update back and
   nothing else has changed yet.
2. `ADMIN_PROFILE=default 00-deployer.sh`, then wait one minute.
3. From `main`, as the deployer: `02-build.sh`, `03-deploy.sh`, `scripts/console/deploy.sh`,
   `04-smoke.sh`, `scripts/console/smoke.sh`. Both builds zip with fresh file times, so both
   deploys upload new code under their prefix and CloudFormation reads the templates from there.
   The one tag-gated write this step exercises is the Console's `create-invalidation` on its
   tagged distribution; the KMS and CloudFront writes that a change would need (`PutKeyPolicy`,
   `TagResource`, `UpdateDistribution`) are shown only by the simulator in step 4.
4. The proofs below, read-only, under the admin profile in a subshell, so it does not leak into
   later deploys. The simulator does not read the resources' tags: it evaluates the policy against
   the tag value given as context. So it runs on auth-gate's own key and distribution twice, once
   with `app = auth-gate` (expect `allowed`) and once with another value (expect
   `implicitDeny`); the real tags are read at the end.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
acct="$(aws sts get-caller-identity --query Account --output text)"
deployer="arn:aws:iam::$acct:user/auth-gate-deployer"
bucket=aws-sam-cli-managed-default-samclisourcebucket-m5yrn5zzyiu6
res() { aws cloudformation describe-stack-resource --stack-name "$1" --logical-resource-id "$2" \
  --query StackResourceDetail.PhysicalResourceId --output text; }
key="$(aws kms describe-key --key-id "$(res "auth-gate-$STAGE" SigningKey)" --query KeyMetadata.Arn --output text)"
dist="arn:aws:cloudfront::$acct:distribution/$(res "auth-gate-$STAGE" Distribution)"
sim() { aws iam simulate-principal-policy --policy-source-arn "$deployer" "$@" \
  --query 'EvaluationResults[].[EvalActionName,EvalResourceName,EvalDecision]' --output table; }
app() { printf 'ContextKeyName=aws:ResourceTag/app,ContextKeyValues=%s,ContextKeyType=string' "$1"; }

# Expect allowed.
sim --action-names kms:PutKeyPolicy kms:TagResource --resource-arns "$key" --context-entries "$(app auth-gate)"
sim --action-names cloudfront:UpdateDistribution cloudfront:TagResource --resource-arns "$dist" --context-entries "$(app auth-gate)"
sim --action-names s3:PutObject --resource-arns "arn:aws:s3:::$bucket/auth-gate-$STAGE/x"
# Expect implicitDeny: the same resources tagged for another project, and outside the prefix.
sim --action-names kms:PutKeyPolicy kms:TagResource --resource-arns "$key" --context-entries "$(app other-project)"
sim --action-names cloudfront:UpdateDistribution cloudfront:TagResource --resource-arns "$dist" --context-entries "$(app other-project)"
sim --action-names s3:PutObject --resource-arns "arn:aws:s3:::$bucket/other-project/x"

# The tags the allowed cases assume, and the table protection.
aws kms list-resource-tags --key-id "$key" --query Tags
aws cloudfront list-tags-for-resource --resource "$dist" --query Tags.Items
aws dynamodb describe-table --table-name "auth-gate-$STAGE" --query Table.DeletionProtectionEnabled
)
```

### First deploy of a stage

A deploy that creates the stacks runs under the admin profile: a new stage's first deploy, and
stg rebuilt after `99-purge.sh` and `scripts/console/purge.sh`. Under the deployer it will fail.
KMS authorises the `Tags` of `CreateKey` as `kms:TagResource`, which must come from an IAM
policy not restricted to particular keys, and its key-policy lockout check needs `PutKeyPolicy`
in the same way (commit `d06865b` hit that). The deployer holds both only on keys already
tagged `app = auth-gate`. The Console's first pass creates its tagged distribution the same
way, so it runs as admin too:

```bash
AWS_PROFILE=default scripts/setup-and-deploy/03-deploy.sh
AWS_PROFILE=default scripts/console/deploy.sh
```

Every later deploy, including the Console's second pass, runs as the deployer.

## Continuous deploy

Every push to `main` runs the `deploy` job in `.github/workflows/ci.yml` after `check` passes,
for stg only. It assumes the role `ci-auth-gate-deploy` through GitHub OIDC (no stored key),
runs `scripts/ci/preflight.sh`, then `02-build.sh`, `03-deploy.sh`, `04-smoke.sh`,
`scripts/console/deploy.sh`, `scripts/console/smoke.sh`, `scripts/portal/deploy.sh` and
`scripts/portal/smoke.sh`. The preflight stops the job unless all three stacks exist in
`CREATE_COMPLETE`, `UPDATE_COMPLETE` or `UPDATE_ROLLBACK_COMPLETE` and the Console's and the
Portal's `AuthGateClientId` output is set: CI never creates a stack, so the Portal's first deploy
is by hand ([Rollout of P2](#rollout-of-p2-the-portal)). The Portal deploy reads the repository
variables `PORTAL_EMAIL_FROM` and `PORTAL_TURNSTILE_SITE_KEY` (Settings, Secrets and variables,
Actions, Variables). Deploys queue (group
`deploy-stg`, never cancelled), so a second merge waits for a running deploy. GitHub keeps one pending run, so a third merge
replaces the queued second and the newest merge wins. If a smoke fails
after a successful deploy, the new code is live: fix forward or revert the PR, whose merge
deploys too.

### Set up

Once, under an admin profile, before the first merge that carries the `deploy` job:

```bash
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
ADMIN_PROFILE=default scripts/ci/00-sam-lifecycle.sh
```

`00-ci-role.sh` creates the GitHub OIDC provider if the account has none and the role, trusted
only for `repo:huy090202@100850219/auth-gate@1423990644:ref:refs/heads/main` (`infra/ci-trust.json`), with the managed
policies `auth-gate-deployer-policy` and `auth-gate-deployer-edge-policy`. `00-sam-lifecycle.sh` adds two lifecycle rules,
`auth-gate-artifacts-*`, to the shared SAM bucket and keeps every rule already there. Both are
rerunnable. The proofs, read-only, under the admin profile in a subshell:

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1
acct="$(aws sts get-caller-identity --query Account --output text)"
role=ci-auth-gate-deploy
aws iam get-role --role-name "$role" --query Role.AssumeRolePolicyDocument
aws iam list-attached-role-policies --role-name "$role" --query 'AttachedPolicies[].PolicyName'
bucket="$(aws cloudformation describe-stacks --stack-name aws-sam-cli-managed-default \
  --query 'Stacks[0].Outputs[?OutputKey==`SourceBucket`].OutputValue | [0]' --output text)"
aws s3api get-bucket-lifecycle-configuration --bucket "$bucket" --query 'Rules[].ID'
)
```

Expect the trust policy with `aud` `sts.amazonaws.com` and the `sub` above, the policies
`auth-gate-deployer-policy` and `auth-gate-deployer-edge-policy`, and both `auth-gate-artifacts-*` rule IDs next to every rule that
was there before. A green first run of the `deploy` job proves the rest.

A shell that exports `AWS_ACCESS_KEY_ID` makes the scripts use those credentials instead of the
`auth-gate-deployer` profile (`lib.sh` sets the profile only when no key is in the environment).
That is how CI hands over the role's credentials; on a workstation, unset it first.

### If a deploy gets stuck

A failed update rolls back by itself. If the rollback fails too, the stack is
`UPDATE_ROLLBACK_FAILED` and the preflight refuses it. Recover by hand under the admin profile:

```bash
aws cloudformation continue-update-rollback --stack-name auth-gate-stg \
  --resources-to-skip <LogicalId>
```

Pick the `<LogicalId>` from the stack's failed events, and repeat for `auth-gate-console-stg` if
that one is stuck. Then rerun the newest failed run (rerunning an older run after newer merges
deploys older code), or push a new commit.

A deploy can also fail when stg has been idle for more than 30 days: the lifecycle rule has then
expired the previous code zip that CloudFormation still references. Fix it the same way, by
running a fresh deploy.

### What stays manual

`00-deployer.sh`, a stage's first deploy (and the Console's and the Portal's second pass), purges,
`register-client.sh` (the Console's and the Portal's), `05-backfill-clients.sh`, `06-prune-clients.sh` and `07-prune-factors.sh`.

### Rollout of P1 (tenant-aware AuthGate)

This branch adds the `portal-key` parameter, the portal and owner routes, soft delete, and splits
the deployer policy in two. It needs a person at an admin profile, in this order. Steps 1 and 2
run back to back from the `feat-self-service` worktree, with no merge to `main` in between:
`00-deployer.sh` rewrites `auth-gate-deployer-policy` without the CloudFront rights, which
the CI role already holds through it, and until `00-ci-role.sh` has attached
`auth-gate-deployer-edge-policy` to that role, a CI deploy would fail on CloudFront.

Before the merge:

```bash
# 1. portal-key; the other two are kept
scripts/setup-and-deploy/01-ssm.sh
# 2. the policy split, then the CI role (no right is added or removed)
ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
```

Then merge. CI deploys and runs the smokes, which now delete the clients they create (step 3).
After the deploy is green, with the admin profile:

```bash
# 4. prune the test clients, then the factors of every deleted client
ADMIN_PROFILE=default scripts/setup-and-deploy/06-prune-clients.sh
ADMIN_PROFILE=default scripts/setup-and-deploy/07-prune-factors.sh
ADMIN_PROFILE=default scripts/setup-and-deploy/07-prune-factors.sh --apply
```

On stg `06-prune-clients.sh` should propose 52 of 55 clients and keep `auth-app-local`,
`auth-app-stg` and `auth-gate-console-stg`. If the count differs, answer anything but
`delete 52` first (it then deletes nothing) and send the listing. `07-prune-factors.sh` without
`--apply` only prints counts; read them before `--apply`.

5. The proofs, read-only, under the admin profile in a subshell, so it does not leak into later
   deploys. Expect both policies on the user and on the role, the three parameters, the client
   list `auth-app-local`, `auth-app-stg` and `auth-gate-console-stg`, and `401` for the admin
   key on a portal route:

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
aws iam list-attached-user-policies --user-name auth-gate-deployer --query 'AttachedPolicies[].PolicyName'
aws iam list-attached-role-policies --role-name ci-auth-gate-deploy --query 'AttachedPolicies[].PolicyName'
aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=/credo/auth-gate/$STAGE/" \
  --query 'Parameters[].[Name,Type]' --output text
base="$(aws cloudformation describe-stacks --stack-name "auth-gate-$STAGE" \
  --query 'Stacks[0].Outputs[?OutputKey==`BaseUrl`].OutputValue | [0]' --output text)"
k="$(aws ssm get-parameter --name "/credo/auth-gate/$STAGE/admin-key" --with-decryption \
  --query Parameter.Value --output text)"
printf 'x-internal-access-key: %s\n' "$k" | curl -sS -H @- "${base%/}/api/clients" | jq -r '.clients[].name'
printf 'x-internal-access-key: %s\n' "$k" | curl -sS -o /dev/null -w '%{http_code}\n' -H @- \
  -H 'x-tenant-id: 01JD0000000000000000000000' "${base%/}/api/portal/clients"
)
```

### When a PR changes rights

A template that needs a new right: run `00-deployer.sh` from the branch before merging, or the CI
deploy fails. A policy that narrows rights: merge first, then apply, as in the infra-hardening
rollout.

## Repository layout

```text
cmd/auth-gate/        ROLE selects the mux; Lambda adapter and local dev server
cmd/backfill-clients/ one-off: write the list-index item for existing clients
cmd/console/          Console Lambda; `console admin ...` operator commands
cmd/portal/           Portal Lambda and its local dev server
cmd/smoke/            WebAuthn round trip against a deployed stage
internal/platform/   foundation: dynamo, kms, ssm, httpx, config, log, ratelimit, and the parts the
                     Console and Portal share (websession, lockout, lease, firstfactor, auditlog,
                     password, webapi, viewerip)
internal/auth/       domain: client, pin, webauthn, challenge, token, handlers
internal/console/    Console: accounts, sessions, step-up, audit, handlers, authgate client
internal/portal/     Portal: accounts, sessions, workspaces, clients, mail, avatars, turnstile, hibp
console-web/  portal-web/   the web apps: React, Vite, Mantine, EN/VI strings
packages/web-kit/   shared web code (theme, UI parts, API client, i18n, WebAuthn) for the Console and the Portal
infra/template.yaml  SAM: three functions, table, KMS key, CloudFront door
infra/console.yaml   SAM: the Console stack
infra/portal.yaml    SAM: the Portal stack
infra/*.json         deployer policy and the permissions boundary
scripts/setup-and-deploy/  the manual deploy and smoke steps
scripts/console/     the Console's deploy, register, admin, smoke and purge steps
scripts/portal/      the Portal's deploy, register, smoke and purge steps
bruno/  bruno-console/  bruno-portal/  integration collections (AuthGate; the Console API; the Portal API)
docs/memo/           one decision per file
docs/infra/          the worktree tooling (below)
docs/adr/  docs/superpowers/   ADRs for the tooling; specs and plans
tools/wt/  .claude/  worktree scripts; Claude Code hooks and commands
```

`internal/platform` never imports `internal/auth`.

## Worktree tooling

This repository is developed as a bare clone with one folder per branch. The scripts in
`tools/wt/` and the workflow are documented in [`docs/infra/`](docs/infra/00-overview.md).
A plain `git clone` also works for contributing.

## Gate and tooling

You need Node.js 22 or newer, pnpm 10.13.1, `jq` and `git`.

If corepack's pnpm shim fails with "Cannot find matching keyid", install pnpm directly
(see `docs/infra/05-pnpm.md`):

```bash
corepack disable pnpm
npm install -g pnpm@10.13.1
```

Then:

```bash
pnpm install
pnpm run check   # lint, shell tests, then make check (Go)
```

## License

MIT. See [LICENSE](LICENSE).
