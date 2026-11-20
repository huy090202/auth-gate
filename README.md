# auth-gate

auth-gate is a two-factor verification service for other backends: a PIN or a WebAuthn
passkey, checked on AWS Lambda, answered with a short-lived token signed by AWS KMS.
A calling backend (a *client*) already knows its user (a *subject*). It asks auth-gate to
enroll and later verify a second factor, and attaches the returned token to whatever the
check was for: a step-up before a sensitive action, the second step of a login, or a
passkey-only login. Browsers never call auth-gate; only backends do.

Operators register and manage clients from the [Console](#the-console), a separate web app
with its own stack. It reaches auth-gate over HTTP like any other client.

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
| `/api/clients*` | `admin` | `x-internal-access-key: <admin key>` |
| `/.well-known/*` | `jwks` | none |
| everything else | `verify` | `x-internal-access-key: <client key>` |

The Function URLs behind the door refuse direct calls: CloudFront adds a secret
`x-origin-verify` header and `verify` and `admin` answer 401 without it. See
[one-door-cloudfront](docs/memo/one-door-cloudfront.md) and
[three-roles-one-binary](docs/memo/three-roles-one-binary.md).

## Credential model

- The **admin key** (SSM SecureString, read at cold start) creates clients and rotates
  their keys.
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

The client list reads an index item written with each client. Clients created before it
existed need `05-backfill-clients.sh` once per stage.

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

1. `deploy.sh`, first pass: writes the `origin-verify` parameter if absent, builds the Lambda
   and the web app, deploys the stack, uploads the site and invalidates the cache. With no
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
`admin-key` from SSM, edit any CloudFront cache, origin request, response headers or origin
access control policy in the account (CloudFront cannot scope those), and it holds wide rights on
Route 53 and ACM until the custom domain work settles them. Keep the deployer key out of CI and
shells you do not control.

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

The deploy and smoke steps are manual and run by a person with AWS access. They live in
`scripts/setup-and-deploy/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION` (default `ap-southeast-1`):

0. `00-deployer.sh` (once per account and after changing either JSON, with an admin profile:
   `ADMIN_PROFILE=default`): create or update the permissions boundary `auth-gate-boundary` from
   `infra/role-boundary.json`, then the IAM user `auth-gate-deployer` with
   `infra/deployer-policy.json`, scoped to `auth-gate-*` resources, and a local AWS profile of
   the same name.
1. `00-cert.sh DOMAIN` (optional): request an ACM certificate in `us-east-1`.
2. `01-ssm.sh`: write the random `admin-key` (SecureString) and `origin-verify` (plain
   String; CloudFront cannot resolve a SecureString).
3. `02-build.sh`: cross-compile `bin/bootstrap.zip` for linux/arm64.
4. `03-deploy.sh`: deploy the SAM stack `auth-gate-<stage>` and print the one `BaseUrl`.
   Without a custom domain the first deploy runs twice: it reads the distribution domain,
   then redeploys to set `BASE_URL`, which avoids a CloudFormation dependency cycle.
5. `04-smoke.sh`: run the Bruno collection, then `cmd/smoke`, a real WebAuthn round trip.
6. (once, after the first deploy that has client listing) `05-backfill-clients.sh`
   (`ADMIN_PROFILE=default`): write the list-index item for clients created before it
   existed. It needs `Scan`, which no Lambda role or the deployer has, so it runs under the
   operator profile. Safe to rerun; prints counts only.
7. `99-purge.sh`: tear the stage down, after a typed confirmation. AuthGate's table has
   deletion protection (it holds every client and every user's factors), so a plain
   `delete-stack` fails; `99-purge.sh` turns it off just before the delete.

Prerequisites: AWS CLI v2 and SAM CLI. A new AWS account may refuse the distribution with
"Your account must be verified before you can add new CloudFront resources"; only AWS Support
can lift that (open an Account and billing case), so do it before the first deploy.

If a deploy fails, CloudFormation rolls back and leaves the stack in `ROLLBACK_COMPLETE`
(or `ROLLBACK_FAILED`), which cannot be updated: delete it with
`aws cloudformation delete-stack --stack-name auth-gate-<stage>` before deploying again. A
missing deployer permission goes into `infra/deployer-policy.json`; rerun `00-deployer.sh`.
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

- **S3:** both deploys upload to their own prefix of the SAM bucket the account shares
  (`--s3-prefix auth-gate-<stage>`, `auth-gate-console-<stage>`). The deployer may read and write
  objects only under `auth-gate-*` there, and list only that prefix. The bucket's random
  suffix is a wildcard, which IAM lets span `/`, so a key that merely contains `/auth-gate-`
  also matches. Artifacts of deploys from before the prefix stay at the bucket root; the
  deployer can no longer read them.
- **KMS:** every right on a key requires the tag `app = auth-gate`, including `PutKeyPolicy`,
  `TagResource` and `DescribeKey`, so tagging cannot pull another project's key into scope.
  `CreateKey` requires the request to carry that tag.
- **CloudFront:** both distributions carry `app = auth-gate`. Reading, changing, deleting,
  tagging and invalidating a distribution requires that tag; creating one requires it in the
  request. Cache, origin request, response headers and origin access control policies stay
  account-wide: CloudFront has no tag or name condition for them.
- **Logs:** reading log events only from `/aws/lambda/auth-gate-*`.

The tags, the prefix and AuthGate's table protection must be in AWS before the narrowed policy
is, so the first rollout runs in this order:

1. From a tree with these changes and the old policy still applied: `02-build.sh`,
   `03-deploy.sh`, then `scripts/console/deploy.sh`. The old policy already allows the tags,
   the new prefix and `dynamodb:UpdateTable`.
2. `ADMIN_PROFILE=default 00-deployer.sh`, then wait one minute.
3. As the deployer: `03-deploy.sh`, `scripts/console/deploy.sh`, `04-smoke.sh`,
   `scripts/console/smoke.sh`. Apart from the Console's invalidation these are empty change
   sets, so step 4 covers the write paths.
4. The proofs below, read-only, under the admin profile. The simulator reads the policy, not the
   resources' tags, so the tags go in as context and the real tags are read separately. It runs
   in a subshell, so the admin profile does not leak into later deploys.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
acct="$(aws sts get-caller-identity --query Account --output text)"
deployer="arn:aws:iam::$acct:user/auth-gate-deployer"
res() { aws cloudformation describe-stack-resource --stack-name "$1" --logical-resource-id "$2" \
  --query StackResourceDetail.PhysicalResourceId --output text; }
key="$(aws kms describe-key --key-id "$(res "auth-gate-$STAGE" SigningKey)" --query KeyMetadata.Arn --output text)"
dist="arn:aws:cloudfront::$acct:distribution/$(res "auth-gate-$STAGE" Distribution)"
bucket="$(aws cloudformation describe-stacks --stack-name aws-sam-cli-managed-default \
  --query "Stacks[0].Outputs[?OutputKey=='SourceBucket'].OutputValue" --output text)"
other_key="$(aws kms list-keys --query "Keys[?KeyArn!='$key'] | [0].KeyArn" --output text)"
[ "$other_key" != None ] || other_key="arn:aws:kms:$AWS_REGION:$acct:key/00000000-0000-0000-0000-000000000000"
other_dist="$(aws cloudfront list-distributions \
  --query "DistributionList.Items[?ARN!='$dist'] | [0].ARN" --output text)"
[ "$other_dist" != None ] || other_dist="arn:aws:cloudfront::$acct:distribution/ENOTAUTHGATE"
sim() { aws iam simulate-principal-policy --policy-source-arn "$deployer" "$@" \
  --query 'EvaluationResults[].[EvalActionName,EvalResourceName,EvalDecision]' --output table; }
tagged=(--context-entries "ContextKeyName=aws:ResourceTag/app,ContextKeyValues=auth-gate,ContextKeyType=string")

# Expect allowed.
sim --action-names kms:PutKeyPolicy --resource-arns "$key" "${tagged[@]}"
sim --action-names cloudfront:UpdateDistribution --resource-arns "$dist" "${tagged[@]}"
sim --action-names s3:PutObject --resource-arns "arn:aws:s3:::$bucket/auth-gate-$STAGE/x"
# Expect implicitDeny: no tag, and outside the prefix.
sim --action-names kms:PutKeyPolicy kms:TagResource --resource-arns "$other_key"
sim --action-names cloudfront:UpdateDistribution cloudfront:TagResource --resource-arns "$other_dist"
sim --action-names s3:PutObject --resource-arns "arn:aws:s3:::$bucket/other-project/x"

# The tags the allowed cases assume, and the table protection.
aws kms list-resource-tags --key-id "$key" --query Tags
aws cloudfront list-tags-for-resource --resource "$dist" --query Tags.Items
aws dynamodb describe-table --table-name "auth-gate-$STAGE" --query Table.DeletionProtectionEnabled
)
```

A new stage's first deploy creates the key and the distributions, and neither KMS nor CloudFront
documents whether a resource's own creation tags satisfy `aws:ResourceTag` in the checks made
during or just after creation (`kms:TagResource`, the key-policy lockout check and
`kms:CreateAlias`; `cloudfront:TagResource` and `GetDistribution`). If that first deploy fails with `AccessDenied` on one of them, clear the failed stack with
`99-purge.sh` or `scripts/console/purge.sh` and run the first deploy under an admin profile;
later deploys run as the deployer.

## Repository layout

```text
cmd/auth-gate/        ROLE selects the mux; Lambda adapter and local dev server
cmd/backfill-clients/ one-off: write the list-index item for existing clients
cmd/console/          Console Lambda; `console admin ...` operator commands
cmd/smoke/            WebAuthn round trip against a deployed stage
internal/platform/   foundation: dynamo, kms, ssm, httpx, config, log, ratelimit
internal/auth/       domain: client, pin, webauthn, challenge, token, handlers
internal/console/    Console: accounts, sessions, step-up, audit, handlers, authgate client
console-web/         Console web app: React, Vite, Mantine, EN/VI strings
infra/template.yaml  SAM: three functions, table, KMS key, CloudFront door
infra/console.yaml   SAM: the Console stack
infra/*.json         deployer policy and the permissions boundary
scripts/setup-and-deploy/  the manual deploy and smoke steps
scripts/console/     the Console's deploy, register, admin, smoke and purge steps
bruno/  bruno-console/  integration collections (AuthGate; the Console API)
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
