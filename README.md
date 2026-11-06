# auth-gate

auth-gate is a two-factor verification service for other backends: a PIN or a WebAuthn
passkey, checked on AWS Lambda, answered with a short-lived token signed by AWS KMS.
A calling backend (a *client*) already knows its user (a *subject*). It asks auth-gate to
enroll and later verify a second factor, and attaches the returned token to whatever the
check was for: a step-up before a sensitive action, the second step of a login, or a
passkey-only login. Browsers never call auth-gate; only backends do.

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
- A client key on an admin route is a 401, and the 401 body never names the key or header.

Each client registers its `rpId` and `origins`, so it can only verify passkeys for its
own domains. A subject is an opaque string chosen by the client; ids containing `#` are
rejected. PINs are argon2id hashed and locked after repeated failures.

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
cd .. && npx -y @usebruno/cli run --env local --sandbox=developer
```

`make check` runs `go vet`, `golangci-lint run` and `go test ./...`; `make test-store`
also runs the store tests against DynamoDB Local.

## Deploy

The deploy and smoke steps are manual and run by a person with AWS access. They live in
`scripts/setup-and-deploy/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION` (default `ap-southeast-1`):

0. `00-deployer.sh` (once per account, with an admin profile: `ADMIN_PROFILE=default`): create
   the IAM user `auth-gate-deployer` with `infra/deployer-policy.json`, scoped to `auth-gate-*`
   resources, and a local AWS profile of the same name.
1. `00-cert.sh DOMAIN` (optional): request an ACM certificate in `us-east-1`.
2. `01-ssm.sh`: write the random `admin-key` (SecureString) and `origin-verify` (plain
   String; CloudFront cannot resolve a SecureString).
3. `02-build.sh`: cross-compile `bin/bootstrap.zip` for linux/arm64.
4. `03-deploy.sh`: deploy the SAM stack `auth-gate-<stage>` and print the one `BaseUrl`.
   Without a custom domain the first deploy runs twice: it reads the distribution domain,
   then redeploys to set `BASE_URL`, which avoids a CloudFormation dependency cycle.
5. `04-smoke.sh`: run the Bruno collection, then `cmd/smoke`, a real WebAuthn round trip.
6. `99-purge.sh`: tear the stage down.

Prerequisites: AWS CLI v2 and SAM CLI. A new AWS account may refuse the distribution with
"Your account must be verified before you can add new CloudFront resources"; only AWS Support
can lift that (open an Account and billing case), so do it before the first deploy.

If a deploy fails, CloudFormation rolls back and leaves the stack in `ROLLBACK_COMPLETE`
(or `ROLLBACK_FAILED`), which cannot be updated: delete it with
`aws cloudformation delete-stack --stack-name auth-gate-<stage>` before deploying again. A
missing deployer permission goes into `infra/deployer-policy.json`; rerun `00-deployer.sh`.

## Repository layout

```text
cmd/auth-gate/       ROLE selects the mux; Lambda adapter and local dev server
cmd/smoke/           WebAuthn round trip against a deployed stage
internal/platform/   foundation: dynamo, kms, ssm, httpx, config, log
internal/auth/       domain: client, pin, webauthn, challenge, token, handlers
infra/template.yaml  SAM: three functions, table, KMS key, CloudFront door
scripts/setup-and-deploy/  the manual deploy and smoke steps
bruno/               integration collection (the handoff for integrators)
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
