# Self-service sign-up: the Portal and a tenant-aware AuthGate

Date: 2026-11-22. Status: approved by the product owner in chat, section by section, 2026-11-20 to
2026-11-22.

## 1. Purpose

Today an operator creates every AuthGate client in the Console. This change lets an individual or
an organisation sign up, verify an email address, and manage their own clients and keys, with only
the rights they need. It is the phase deferred on 2026-11-06 (Console design, section 1).

Context that shapes every choice: there are no users yet, and the project is the owner's interview
portfolio. Anything with a fixed monthly cost is built but ships switched off. Pay-per-use cost is
fine. The design is complete rather than a cut-down first version.

Decided by the product owner (not re-askable):

- Use is immediate after email verification. There is no approval step.
- Organisations have members. A person can belong to many organisations and has a personal
  workspace of their own.
- Roles are `owner`, `admin` and `member`, with the rights in section 6.
- Sign-in is a password plus a mandatory PIN or passkey, with recovery codes and password reset by
  email.
- Email goes through Amazon SES.
- Abuse controls: Cloudflare Turnstile, rate limits per IP and per email, quotas, and AWS WAF built
  but off.
- Quotas: 3 organisations created per person (the personal workspace not counted), 5 clients per
  tenant, 20 members and 20 pending invites per organisation, and `rateLimit` burst 20 at 10 per
  second for clients made through the Portal.
- Architecture: a separate Portal app, and a scoped key in AuthGate (section 3).
- Delivery in four phases, each with its own plan and PR (section 11).

A design that does not depend on the domain name: the Portal runs on its CloudFront domain until
the custom domain exists, then moves to `portal.<domain>`.

## 2. Non-goals

Billing; SSO or sign-in with Google or GitHub; a `viewer` role; moving a client between tenants;
data export; WAF switched on; a production stage.

## 3. Architecture

```
person ──► CloudFront (Portal URL)
             ├─ /api/*      → Lambda Function URL (portal, Go, arm64; x-origin-verify)
             ├─ /avatars/*  → S3 (avatar bucket)
             └─ /*          → S3 (SPA, portal-web)
portal ──► DynamoDB auth-gate-portal-<stage>  (users, tenants, members, invites, sessions, audit)
portal ──► AuthGate /api/portal/*              (portal key: only tenant-owned clients)
portal ──► AuthGate /api/challenges, /factors… (its own client key: Portal users' PIN and passkeys)
portal ──► AuthGate /.well-known/jwks.json
portal ──► SES (email), Turnstile siteverify, Have I Been Pwned range API
console ──► AuthGate /api/clients*, /api/owners/*  (admin key)
console ──► Portal /api/operator/*                 (portal-operator key)
```

Three apps, three audiences:

| App | Who | What for |
|---|---|---|
| auth-app | developers evaluating AuthGate | docs and the sample app |
| Portal | customers (people and organisations) | accounts, organisations, their own clients and keys |
| Console | the operator | every client and tenant, quotas, rate limits, suspensions, operator accounts |

Rules:

- The Portal never holds the admin key. Its portal key reaches only `/api/portal/*`, where AuthGate
  itself checks that the client belongs to the tenant named in the request. A Portal bug can at
  worst confuse two tenants. It cannot touch an operator's client or change a rate limit.
- `cmd/portal` is its own binary and reaches AuthGate only over HTTP, like the Console.
- The Console never reads or writes the Portal table. It uses the Portal's operator API.
- The Portal is an AuthGate client with its own `rpId`, set up with the same two-pass deploy as
  the Console. Its users are that client's subjects (dogfooding).
- Repository layout: `cmd/portal/`, `internal/portal/`, `portal-web/`, `infra/portal.yaml`,
  `scripts/portal/`, `bruno-portal/`. UI components the Console and the Portal share move to a
  workspace package rather than being copied.

## 4. Changes to AuthGate

### 4.1 Ownership

- `Client` gains `owner`: empty for an operator's client (the Console, auth-app, every existing
  client), `T#<tenantId>` for a client made through the Portal. It is set at creation and never
  changes, through any key.
- Creating an owned client also writes `pk=OWNER#<tenantId>`, `sk=C#<clientId>` (name, rpId,
  createdAt, disabledAt) in the same transaction as the client and its `CLIENTS` index item.
- `pk=OWNER#<tenantId>`, `sk=META` holds `clientCount`, `clientQuota` (default 5) and
  `suspendedAt`. A create increments `clientCount` in the same transaction, on the condition
  `clientCount < clientQuota`, else `409 quota_exceeded`. A delete decrements it.
- `disabledBy` records `operator` or `tenant`. A tenant cannot enable a client an operator
  disabled.
- `deletedAt`: a deleted client's keys answer `401`, it leaves every list, and it frees a quota
  slot. Its factor items (`S#<clientId>#<subject>`) stay: no key reaches them. An operator script,
  run by hand with `Scan` like `05-backfill-clients.sh`, removes them.

### 4.2 The portal routes

A second set of routes on the admin Lambda, guarded by `portal-key` (SSM SecureString). The admin
key does not open them and the portal key does not open `/api/clients*`. Every request carries
`x-tenant-id`; a client whose `owner` is not `T#<x-tenant-id>`, or is empty, answers
`404 client_not_found`, the same as an unknown client.

| Route | Notes |
|---|---|
| `POST /api/portal/clients` | Sets `owner`. `rateLimit` is forced to burst 20, 10 per second; a body field is ignored. |
| `GET /api/portal/clients` | Reads the `OWNER#` index only. |
| `GET /api/portal/clients/{id}` | |
| `PATCH /api/portal/clients/{id}` | `name`, `origins`, `pinPolicy`, `tokenTtlSec`. `rateLimit` answers `400 invalid_client`. |
| `POST /api/portal/clients/{id}/disable` and `/enable` | Enable refuses `disabledBy = operator` with `409 disabled_by_operator`. |
| `POST /api/portal/clients/{id}/keys` | Rotate. |
| `DELETE /api/portal/clients/{id}/keys/previous` | |
| `DELETE /api/portal/clients/{id}/subjects/{subjectId}` | Purge one end user's factors. |
| `DELETE /api/portal/clients/{id}` | Soft delete, as in 4.1. |

A suspended owner refuses every write with `409 tenant_suspended`.

### 4.3 Operator routes

Under the admin key: `GET` and `PATCH /api/owners/{tenantId}` (read and change the quota),
`POST /api/owners/{tenantId}/suspend` and `/unsuspend`. `GET /api/clients` gains `owner` in each
summary.

Also under the admin key, asked for by the owner on 2026-11-22 to clean stg up:

- `DELETE /api/clients/{id}`: the same soft delete as the portal route, for any client. The
  Console's own client and the Portal's are protected by the Console, which answers
  `409 own_client_protected` (and later by the Portal's own guard). AuthGate's admin `DELETE` has
  no such guard: a direct call with the admin key deletes any client. The Console gains a Delete
  action on a client, with step-up and the client's name typed again.
- The smoke runs delete what they create: `cmd/smoke` and the Bruno collection end by deleting
  their clients, so CI no longer adds six clients to stg on every merge (`bruno boundary A`, `B`,
  `C`, `bruno demo renamed`, `bruno rate limit`, `smoke`).
- `scripts/setup-and-deploy/06-prune-clients.sh`, run by hand under the admin profile: lists every
  client with its id, name, rpId, creation date and owner; proposes the ones named `smoke`,
  `bruno boundary A`, `bruno boundary B`, `bruno boundary C`, `bruno demo`, `bruno demo renamed`,
  `bruno rate limit`, `bruno portal` or `rate-limit-probe`; lets the operator add or drop ids; never offers the
  Console's or the Portal's own client; and deletes only after a typed confirmation. On stg on
  2026-11-22 that is 52 of 55 clients; `auth-app-local`, `auth-app-stg` and
  `auth-gate-console-stg` stay.

### 4.4 Verification

The key lookup also refuses a deleted client and a client whose owner is suspended, with the same
`401` as an unknown key. An owned client's lookup reads its `OWNER#` item, cached for 30 seconds per
Lambda instance, so a suspension takes effect within 30 seconds. Operator clients pay nothing extra.

## 5. The Portal's data

One table, `auth-gate-portal-<stage>`, with no GSI. Two-way links are written in one transaction.

| Item | Key | Holds |
|---|---|---|
| User | `USER#<id>` / `META` | email, password hash, `emailVerifiedAt`, `sessionEpoch`, `orgsCreated`, `suspendedAt`, lockout counters, avatar key |
| Email index | `EMAIL#<normalised email>` / `META` | user id. Makes an email unique. |
| Tenant | `TENANT#<id>` / `META` | `personal` or `org`, name, createdBy, createdAt, `suspendedAt`, `deletingAt` |
| Tenant index | `TENANTS` / `T#<id>` | name, kind, createdAt, for the operator's list |
| Membership | `TENANT#<id>` / `MEMBER#<userId>` and `USER#<userId>` / `TENANT#<tenantId>` | role, joinedAt; the pair lists members of a tenant and tenants of a user |
| Invite | `TENANT#<id>` / `INVITE#<tokenHash>` | email, role, invitedBy, expiresAt (TTL) |
| Email token | `ETOKEN#<tokenHash>` / `META` | purpose (`verify`, `reset`, `factor-reset`, `email-change`), user id, expiresAt (TTL), single use |
| Recovery code | `USER#<id>` / `RECOVERY#<codeHash>` | 10 per user, single use |
| Factor reset | `USER#<id>` / `FACTORRESET` | requestedAt, dueAt |
| Session | `SESSION#<cookieHash>` / `META` | as the Console's, plus the active tenant |
| Audit | `TENANT#<id>` / `AUDIT#<time>#<id>` | actor, action, target, IP; TTL 180 days |
| Rate bucket | `RL#<scope>#<key>` / `META` | the existing token bucket |

Client ownership is not stored here. AuthGate's `owner` is the one source of truth.

Every token sent by email is 32 random bytes; only its SHA-256 is stored. Recovery codes are random
and high-entropy, so SHA-256 is enough for them. Passwords use the Console's password hashing.

## 6. Roles

| Right | owner | admin | member |
|---|---|---|---|
| See the tenant's clients | ✅ | ✅ | ✅ |
| Create a client; change its name and origins | ✅ | ✅ | ✅ |
| Rotate a key; revoke the previous key | ✅ | ✅ | ✅ |
| Disable or enable a client; purge an end user's factors; delete a client | ✅ | ✅ | ❌ |
| Invite and remove members; change roles up to admin | ✅ | ✅ | ❌ |
| Add or remove owners; transfer ownership | ✅ | ❌ | ❌ |
| Rename or delete the organisation | ✅ | ❌ | ❌ |
| Read the organisation's audit log | ✅ | ✅ | ❌ |

- An organisation always has at least one owner. The guard is the Console's master guard: one
  transaction that also checks the actor is still an owner (docs/memo/console-sessions.md).
- A personal workspace has exactly one member, its owner. It takes no invites, cannot be renamed,
  and is deleted only with the account.
- No role changes `rateLimit` or a quota. Only an operator does.
- Step-up (a PIN or passkey within 300 seconds, single use, bound to action and target) guards:
  rotate, revoke previous, disable, purge, delete client, role changes, member removal, ownership
  transfer, organisation deletion, and the account actions in section 7.

## 7. Account flows

### Sign-up

1. Email, display name, password, Turnstile. The password has at least 12 characters and is checked
   against Have I Been Pwned's range API: only the first five hex characters of its SHA-1 leave the
   system. The check times out after 2 seconds; on failure the password is accepted and the miss is
   logged.
2. The answer is always "check your email", whether or not the email has an account.
   - A new email gets a verification link, valid 24 hours.
   - A known email gets "someone tried to sign up with this address; sign in or reset your
     password". The answer on screen is still "check your email"; the mail goes to the account's
     owner, never to the person who typed the address.
3. An unverified account expires after 7 days (TTL on the user and email items), which frees the
   email. A new sign-up for an address whose account is still unverified replaces that account and
   voids its older links. The newest sign-up wins, so someone who signs up with another person's
   address cannot hold it: the real owner signs up again and takes it.
4. The link, together with the account's password, marks the email verified, creates the personal
   workspace and its membership, and starts a session in stage `enroll`. A wrong password answers
   like a bad link, counts as a failed sign-in and leaves the link usable.

### Sign-in

1. Email and password; Turnstile when section 9 requires it. Any failure answers the same `401`.
   Five failures lock the account for 15 minutes, as the Console does. An unverified email cannot
   sign in, and says so no differently from a wrong password (see
   `2026-11-24-p3-organisations-design.md` section 6); the screen always offers to resend the link.
2. A PIN or passkey through AuthGate. An account with no factor must enrol one before reaching
   `full`. `FactorProved` and the first-factor lease work as in the Console.

Sessions are the Console's: a server-side item keyed by the cookie's hash, the account's
`sessionEpoch` checked on every request. A session also carries the active tenant.

### Recovery codes

Ten single-use codes, shown once, right after the first factor is enrolled. Regenerating them needs
step-up and voids the old set.

### Forgotten password

A link valid 1 hour sets a new password and bumps `sessionEpoch`. The next sign-in still needs a PIN
or passkey: control of the email alone is not enough.

### Lost factor

- With a recovery code: password plus code lands the person in `enroll` (not `full`), with the
  code counted as proof, to add a new factor. In `full`, adding a factor needs step-up with a
  factor the person has just said they have lost.
- Without: a factor reset requested by email link. Confirming it starts a 24-hour wait and emails
  the owner of the account. A successful sign-in with an existing factor during the wait cancels it.
  After the wait, the factors are purged through the Portal's own client, and the next sign-in
  enrols a new one.

### Profile and security

| Action | Needs |
|---|---|
| Change display name | a session |
| Change password | current password and step-up; bumps `sessionEpoch` |
| Change email | step-up and a link sent to the new address; the old address is told; the email index moves in one transaction |
| Add or remove a PIN or passkey | step-up; the last factor cannot be removed |
| Delete account | step-up. Refused while the person is the only owner of an organisation that has other members. Deletes the personal workspace and its clients. |
| Avatar | PNG, JPEG or WebP, at most 1 MB, uploaded with a presigned S3 POST to `pending/` first (a bucket lifecycle rule expires that prefix after 1 day), confirmed by the server, then copied to `avatars/`, which is served at `/avatars/*`. The Console's admins get the same mechanism. |

Security emails are sent for: password changed, email changed, factor added or removed, recovery
code used, factor reset requested, account locked after failed sign-ins.

## 8. Organisations

Refined by `2026-11-24-p3-organisations-design.md`, which wins where the two differ.

- **Create:** a name of 2 to 64 characters; the creator is owner. `orgsCreated` on the user item is
  incremented in the same transaction, on the condition `orgsCreated < 3`. Deleting an organisation
  decrements it.
- **Invite** (owner or admin): an email and a role. An admin invites `admin` or `member`; only an
  owner invites `owner`. The link is single use and valid 7 days. Pending invites can be revoked or
  resent. At most 20 pending invites and 20 members.
- **Accept:** the person must be signed in, or sign up and verify, with exactly the invited email.
  Accepting writes the membership pair and deletes the invite in one transaction, on the condition
  that the organisation has fewer than 20 members. The invitee may decline.
- **Members:**

| Action | Who | Rule |
|---|---|---|
| Change a role | owner: any role; admin: between admin and member | an admin cannot touch an owner |
| Remove a member | owner: anyone; admin: admins and members | never the last owner |
| Leave | anyone | the last owner cannot leave |
| Transfer ownership | owner | promotes a member to owner, with step-up; the former owner can then step down |

- **Delete** (owner, step-up, the organisation's name typed again): `deletingAt` is set first, so the
  tenant is unusable at once. Each client is deleted through `DELETE /api/portal/clients/{id}`, then
  members and invites are removed, and the remaining items get a 30-day TTL. A run that stops part
  way resumes where it stopped.
- **Switching tenants:** URLs carry the tenant (`/t/<tenantId>/clients`), so two tabs on two tenants
  do not mix. Every request checks that the URL's tenant is one the session's user is a member of,
  with the role the action needs.
- **Clients in a tenant:** every operation in section 4.2 goes through the portal key with that
  tenant's id. A new key is shown once (the Console's `ShownOnce`). A client an operator disabled
  shows "Locked by an administrator" and cannot be enabled.
- **Audit:** client create, update, rotate, disable, enable, delete; end-user purge; invite, accept,
  remove, role change; rename. Actor, IP (from `CloudFront-Viewer-Address`) and time. Kept 180 days.

## 9. Abuse controls

No layer here has a fixed cost.

**Turnstile**, secret in SSM, checked with `siteverify`:

| Where | When | If Cloudflare fails |
|---|---|---|
| Sign-up, forgotten password, resend link, factor reset request | always | refuse with `503` |
| Sign-in | after 2 failures in 15 minutes for that IP or account | refuse while it is required |

**Rate limits**, the existing token bucket in the Portal table, `429` with `Retry-After`:

| Action | Per IP | Per email or account |
|---|---|---|
| Sign-up | burst 5, then 1 per 10 minutes | 3 emails per hour |
| Sign-in | burst 10, then 1 per 6 seconds | 5 failures lock for 15 minutes |
| Forgotten password, resend link | 10 per hour | 3 per hour |
| Invites | | 20 per day per organisation |
| Any signed-in API call | | burst 60, then 1 per second per user |

**Quotas** as in section 1; the client quota is enforced by AuthGate (4.1).

**Email:**

- SES account-level suppression list on (bounces and complaints are not sent to again).
- Only fixed, system-written text, plus an organisation name and an inviter name. Both are plain
  text of at most 64 characters, and a value that contains a URL is refused, so an invite cannot
  carry a phishing link.
- Every email says what to do if it was not you.

**WAF, built but off:** a stack `auth-gate-waf-<stage>` in `us-east-1` (CloudFront requires it
there), deployed only when `EnableWaf` is true. It holds a rate-based rule (300 requests per 5
minutes per IP), `AWSManagedRulesCommonRuleSet` and `AmazonIpReputationList`. The Portal and
AuthGate stacks take its web ACL ARN as an optional parameter. Cost when on: about 6 to 10 USD a
month.

**Responses to abuse:** an operator suspends a tenant (4.3) or a user (section 10).

## 10. The operator's side

The Portal exposes `/api/operator/*`, guarded by `portal-operator-key` (SSM SecureString), which
only the Console reads. The Console's new screens:

- **Tenants:** every tenant from the `TENANTS` index, with name, kind, owner email, member count,
  clients against quota and status, filtered by name. A tenant's page lists members, clients (from
  AuthGate's `OWNER#` index) and audit. Actions, each with step-up: change the client quota
  (AuthGate), suspend or unsuspend (AuthGate suspends the owner; the Portal marks the tenant, which
  makes its UI read-only), and raise a tenant client's `rateLimit` through the existing client edit.
- **Users:** found by exact email through `EMAIL#`. No screen browses every user. It shows the
  user's tenants, verification state, factor count and last sign-in. Actions, each with step-up:
  suspend or unsuspend (bumps `sessionEpoch`), resend verification, and force a factor reset (the
  same 24-hour wait and emails as the self-service flow), for a person who proved who they are some
  other way.
- **Existing screens:** the client list gains an `owner` column and a filter. The Portal's own
  AuthGate client answers `409 own_client_protected`, as the Console's does: rotating or disabling
  it would lock every Portal user out. Console admins can set an avatar.
- **Audit:** operator actions are logged in the Console's audit, and those that touch a tenant
  (suspend, unsuspend, quota) also in that tenant's audit, as "AuthGate administrator", without the
  operator's name.
- An operator cannot see a password or a recovery code (both hashed), cannot sign in as a user, and
  cannot read a client key (shown once at creation; an operator can only rotate it).

## 11. Delivery

| Phase | Scope | Done when |
|---|---|---|
| P1: tenant-aware AuthGate | 4.1 to 4.4, including operator delete, self-cleaning smokes and the prune script; the factor cleanup script; the deployer policy split | smoke proves a tenant cannot reach another tenant's or an operator's client |
| P2: Portal, accounts, personal workspace | stack, sections 5, 7, 9; clients in the personal workspace; CI deploys and smokes the Portal | a stranger signs up, creates a client, gets a key and calls AuthGate with it |
| P3: organisations | section 8 | teams work |
| P4: operator and finish | section 10, the WAF stack (off), the auth-app "Get a key" page | ready to open once the domain exists |

Each phase gets its own plan and PR, deploys on merge, and leaves stg working.

### Deployment

- `infra/deployer-policy.json` is at 5973 of 6144 characters and the Portal needs rights on
  `auth-gate-portal-*` buckets. P1 splits it into two managed policies, attached to both the
  deployer user and `ci-auth-gate-deploy` (`00-deployer.sh` and `00-ci-role.sh`).
- The Portal stack's first deploy runs under the admin profile, as every new stack does, with the
  two-pass client registration the Console uses.
- SSM: `portal-key` (AuthGate), `portal-operator-key` (Portal), the Turnstile secret, and the
  Portal's own client key.
- SES before the domain: stg runs in the SES sandbox and sends only to the owner's verified address;
  `ses:SendEmail` is scoped to that identity. After the domain: a domain identity with DKIM records
  at Cloudflare (DNS only), then a request for production access.
- Turnstile: a free Cloudflare account and a site key for the Portal's hostname. Local development
  uses Cloudflare's test keys.
- When the domain arrives the Portal moves to `portal.<domain>`, its `rpId` changes, and stg
  passkeys are recreated.
- Fixed monthly cost: none.

### Tests and checks

Go tests against DynamoDB Local, each for a failure that would do real harm:

- Tenant isolation: every `/api/portal/*` route with the wrong `x-tenant-id` answers `404`; the
  portal key cannot reach an operator's client; `rateLimit` in a body is ignored or refused.
- Races: concurrent creates do not pass the client quota or the organisation quota; two owners
  acting on each other never leave none.
- Email tokens are single use and expire; an invite is accepted only by the invited email.
- No enumeration: sign-up and forgotten password answer the same whether or not the email exists.
- Sessions: a member removed from an organisation is refused on the next request.

Local development writes email to a mailbox directory, so tests can follow links. `bruno-portal`
covers the boundary cases. `scripts/portal/smoke.sh` runs on stg without reading email: the
boundaries, then a portal-key round trip on a smoke tenant (create, rotate, a cross-tenant call that
must answer `404`, delete). The owner runs the real sign-up in a browser.

### Docs

`docs/memo/portal-tenancy.md`, a decision record in the style of `console-sessions.md`; README
sections for the Portal; the auth-app gains a "Get a key" page linking to the Portal.
