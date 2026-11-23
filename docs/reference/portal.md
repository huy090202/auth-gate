# The Portal

The Portal is the self-service screen: anyone signs up, enrols a second factor, and manages
their own AuthGate clients in a workspace. It is a third app with its own stack,
`auth-gate-portal-<stage>`, its own CloudFront URL and its own table, built like the Console and
reaching AuthGate only over HTTP. Design:
[`docs/superpowers/specs/2026-11-22-self-service-design.md`](../superpowers/specs/2026-11-22-self-service-design.md);
the reasons for the account rules are in [portal-accounts](../memo/portal-accounts.md) and
[client-ownership](../memo/client-ownership.md).

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

## Accounts

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

## Portal API

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

## Run it locally

Needs Docker, Go, Node.js and pnpm as in [Local development](development.md#local-development).

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
to HIBP). The HTML previews in the mailbox directory show no logo, and their Sign-in button leads
nowhere, because `PUBLIC_BASE_URL` points at the API in dev. The Portal client's origin is `http://localhost:5174`, so passkeys need that port. The
Bruno collection for the API is in `bruno-portal/`; `01-boundary` needs no credential:

```bash
cd bruno-portal/environments && cp local.example.yml local.yml
cd .. && ../node_modules/.bin/bru run 01-boundary --env local -r
```

## Deploy

Scripts are in `scripts/portal/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION`. AuthGate must already be deployed in that stage with the
`portal-key` parameter, and the [permissions boundary](deploy.md#permissions-boundary) and the deployer
policies must carry the Portal's rights first ([Rollout of P2](../runbooks/rollout-p2.md#rollout-of-p2-the-portal)).

1. `deploy.sh`, first pass, under the admin profile ([First deploy of a stage](deploy.md#first-deploy-of-a-stage)).
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

CI runs `deploy.sh` and `smoke.sh` after the Console's ([Continuous deploy](deploy.md#continuous-deploy)),
with `EMAIL_FROM` and `TURNSTILE_SITE_KEY` from the repository variables `PORTAL_EMAIL_FROM` and
`PORTAL_TURNSTILE_SITE_KEY`. They are variables, not secrets: the sender address and the site key
are both public. `SUPPORT_EMAIL` (optional) is the Reply-To address and the support line of every
email. It is not an input you export: `deploy.sh` sets it to `support@<domain>` when a domain is configured
([Mail to the domain](custom-domain.md#mail-to-the-domain)). The Lambda has no reserved concurrency (the account
quota is small), so the per-IP and per-account limiters are the guard. While SES is in its sandbox it sends only to
verified addresses.

## Known limits

- A deleted avatar can stay in CloudFront's cache for up to 24 hours.
- A tenant can register any `rpId` and origins for its own client: there is no domain-ownership
  check. The browser still binds a passkey to the origin it was made on.
- Anyone can force a Turnstile challenge on a given address by failing a sign-in for it twice; that
  is friction, not access.
- The four email routes always take about 1.5 seconds.
