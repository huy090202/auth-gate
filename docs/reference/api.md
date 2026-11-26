# The AuthGate API

## One base URL

One Go binary runs as three Lambda functions behind one CloudFront distribution, which
routes by path. Integrators see a single base URL, the distribution's `*.cloudfront.net`
name or an optional custom domain, `api.<domain>` (parameters `DomainName` and
`CertificateArn`, set by `03-deploy.sh` from SSM; DNS is yours to create, see
[Custom domain](custom-domain.md#custom-domain)).

| Path | Role | Credential |
| --- | --- | --- |
| `/api/clients*`, `/api/owners*` | `admin` | `x-internal-access-key: <admin key>` |
| `/api/portal*` | `admin` | `x-internal-access-key: <portal key>` and `x-tenant-id` |
| `/.well-known/*` | `jwks` | none |
| everything else | `verify` | `x-internal-access-key: <client key>` |

The Function URLs behind the door refuse direct calls: CloudFront adds a secret
`x-origin-verify` header and `verify` and `admin` answer 401 without it. See
[one-door-cloudfront](../memo/one-door-cloudfront.md) and
[three-roles-one-binary](../memo/three-roles-one-binary.md).

## Credential model

- The **admin key** (SSM SecureString, read at cold start) creates clients and rotates
  their keys.
- The **portal key** (SSM SecureString, `portal-key`) is the Portal's credential. It opens only
  `/api/portal/*`, where every call names a tenant in `x-tenant-id` and reaches only that
  tenant's clients. The admin key does not open those routes and the portal key does not open
  the admin routes: each answers `401 unauthorized` on the other's
  ([memo](../memo/client-ownership.md)).
- A **client key** (`ag_` plus 32 random bytes) identifies one calling backend. It is
  returned once; only its SHA-256 is stored, and `clientId` is derived from it, never
  taken from the body ([memo](../memo/client-key-is-a-hash-lookup.md)).
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
| `GET /api/clients` | List clients, paged with `?cursor=`. Summaries only, no key material. With `?owner=<tenantId>` it lists only that tenant's clients, from the owner index, with the same summaries and cursor; a malformed id answers `400 invalid_owner`. |
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
([memo](../memo/rate-limit-bucket.md)).

## The WebAuthn relay

The browser runs `navigator.credentials.create/get`, but only against the client's own
backend, which relays to auth-gate
([memo](../memo/webauthn-without-a-browser.md)):

```text
browser ──► client backend ──► POST /api/webauthn/registrations ──► options
browser ◄── client backend ◄──────────────────────────────────────┘
browser (navigator.credentials.create) ──► attestation ──► client backend
                        client backend ──► POST /api/webauthn/registrations/{id} ──► credentialId
```

Verification is the same shape: `POST /api/challenges`, then
`POST /api/challenges/{id}/verify`. A PIN uses the same two calls
([memo](../memo/pin-goes-through-a-challenge.md)). Challenges are single use, live five
minutes and are scoped to the client ([memo](../memo/challenge-is-client-scoped.md)).

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
SSM ([memo](../memo/kms-signs-not-ssm.md)).

## Rotating the signing key

Order matters, because consumers cache the JWKS for 300 s:

1. Create the new KMS key.
2. Add its id to `KMS_KEY_IDS` on the `jwks` function, so it is published first.
3. Switch `KMS_KEY_ID` on the `verify` function to the new key.
4. After 15 minutes (the longest token lifetime) remove the old id from `KMS_KEY_IDS`.
