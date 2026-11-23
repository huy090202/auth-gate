# The Console

The Console is the operator's screen for client work: register and manage clients, rotate
or revoke keys, disable a client, clear one user's factors, and read the audit log. It is a
separate app with its own CloudFront URL and its own stack, `auth-gate-console-<stage>`, so a
Console deploy cannot touch the service and the service gains no browser-facing path. The
browser never holds the admin key; the Console backend reads it from SSM. Design:
[`docs/superpowers/specs/2026-11-06-console-design.md`](../superpowers/specs/2026-11-06-console-design.md).

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

## Sign-in model

- Accounts are created by an operator or a `master`; there is no public sign-up. Roles:
  `admin` manages clients and reads the audit log, `master` also manages admin accounts.
- Sign-in is a password, then a PIN or passkey checked by auth-gate. Unknown user and wrong
  password answer the same 401. Five failures lock the account for 15 minutes.
- The first sign-in enrolls: the temporary password is changed and a first factor added
  before any other screen opens.
- Sessions are server-side (cookie `__Host-console_session`, HttpOnly, SameSite=Strict), 30
  minutes idle and 8 hours absolute. Resetting, clearing or disabling an admin ends that
  admin's sessions at once, because every request compares the session's epoch with the
  account's ([memo](../memo/console-sessions.md)).
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

## Audit

Every write records an audit item, success or failure, with its source (`web` or `cli`), the
actor, the target, the result and a small allow-listed detail (for example the fields a patch
changed, or why a sign-in failed). A target is stored only once it passed validation, and
client values only after auth-gate accepted them. No key, PIN, password or token is stored.
Two kinds of event go to the log, not the table, because they are unbounded: limiter refusals
and failed writes that have no session (other than under `/api/auth`, which is stored with
empty identity). Audit items have no TTL.

## Console API

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

## Interface

A left sidebar that collapses to an icon rail, with the state kept in the browser. The avatar
menu in the header opens My account and Sign out. Preferences on My account holds the
language (English or Vietnamese, English by default) and the appearance (light, dark or
system, system by default).

## Run it locally

Needs Docker, Go, Node.js and pnpm as in [Local development](development.md#local-development).

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

## Deploy

Scripts are in `scripts/console/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION`. AuthGate must already be deployed in that stage; if
the [permissions boundary](deploy.md#permissions-boundary) is new, follow the order there first.

1. `deploy.sh`, first pass, under the admin profile ([First deploy of a
   stage](deploy.md#first-deploy-of-a-stage)): writes the `origin-verify` parameter if absent, builds the
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

## The admin key and the Console

The Console reads the admin key at cold start. Rotating `admin-key` therefore means
redeploying both stacks, `auth-gate-<stage>` and `auth-gate-console-<stage>`.

## If AuthGate is down

Nobody can sign in to the Console, because the second factor is checked by AuthGate. The
emergency path skips the Console: call the [Admin API](api.md#admin-api) directly with the admin
key from SSM, as in the [rate-limit example](api.md#rate-limit).

## Recovering a locked-out master

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

## What a stolen deployer key can do

The permissions boundary caps every `auth-gate-*` IAM role the deployer creates, so a stolen
deployer key cannot mint a role more powerful than the boundary. Its KMS, CloudFront, S3 and log
rights reach only auth-gate's own resources ([Deployer scope](deploy.md#deployer-scope)). It can still
change Lambda code and the Console's site (so it can swap the JavaScript the admin loads), read
`admin-key` and `portal-key` from SSM, edit any CloudFront cache, origin request, response headers or origin
access control policy in the account (CloudFront cannot scope those), and it holds rights on ACM
certificates. CI does not use the key: it assumes the
role `ci-auth-gate-deploy` ([Continuous deploy](deploy.md#continuous-deploy)). Keep the key out of shells
you do not control.
