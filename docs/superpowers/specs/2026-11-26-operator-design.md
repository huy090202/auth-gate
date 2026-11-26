# Self-service P4a: the operator's side — design

Date: 2026-11-26. Status: draft for the owner's review. Branch `feat/operator`.

Refines §10 of `2026-11-22-self-service-design.md` (the parent spec). P4 was split in two:
this branch covers the operator's side, and `feat/waf` follows with the WAF stack, which ships off.
The parent spec's auth-app "Get a key" page is already done: the auth-app docs rewrite of
2026-11-23 sends readers to the Portal's sign-up and walks through creating a client.

## 1. Purpose

Self-service customers live in the Portal, and today the Console cannot see them. An operator
cannot look up a tenant, change its client quota, stop an abusive tenant or user, or help a
person who lost their factors. This branch gives the Console those screens. It does so without
the Console reading the Portal's table, and without anyone gaining the power to read a secret or
sign in as a user.

Decided by the owner on 2026-11-26:

- Any Console admin may run the operator actions. Each one needs step-up and is audited.
  Managing admin accounts stays master-only.
- The people affected get an email when a tenant or user is suspended or unsuspended.
- A suspension needs a reason. The reason is internal: it is kept in the Console's audit and
  shown to other admins, and it is never sent to the customer.

Done means all of this works on stg:

1. An admin suspends a tenant.
   - Every client key of that tenant stops working, within AuthGate's 30-second owner cache.
   - The Portal shows the workspace read-only.
   - The tenant's owners get an email.
2. Unsuspending restores all three.
3. An admin changes the quota, suspends and unsuspends a user, resends a verification email,
   and forces a factor reset.
4. Every action appears in the Console's audit. The tenant-level ones also appear in the
   tenant's own audit as "AuthGate administrator".

## 2. Architecture

```
console ──► AuthGate /api/clients?owner=, /api/owners/*     (admin key, as today)
console ──► Portal   /api/operator/*                        (portal operator key, new)
```

- The Portal exposes `/api/operator/*` behind a new key. The key is an SSM SecureString at
  `/credo/auth-gate-portal/<stage>/operator-key`. Only the Portal and the Console read it. The
  Portal compares it in constant time and rate-limits it, with a bucket per key.
- The Console orchestrates. When an action touches both services, the Console calls AuthGate
  first and the Portal second.
- Both calls are idempotent. When the second call fails, the Console reports the error, and the
  tenant page shows the mismatch with a button that runs the missing step (§5).
- The Console never reads the Portal's table. The Portal never holds the admin key.

## 3. Changes to AuthGate

- `GET /api/clients?owner=<tenantId>` lists one tenant's clients from the existing `OWNER#`
  index. It uses the same summaries and cursor as the unfiltered list.
- Nothing else changes. The routes `GET`/`PATCH /api/owners/{tenantId}` and
  `/suspend`/`/unsuspend` already exist (parent §4.3).

## 4. The Portal

### 4.1 Operator routes

Each route takes the header `x-operator-key`.
- A missing or wrong key gets `401 {"error":"unauthorized"}`, the answer every key check in
  the system gives.
- Write routes are idempotent. Repeating a write answers `200` and sends no second email.

| Route | Result |
|---|---|
| `GET /api/operator/tenants?cursor=` | 50 tenants per page, newest first, from the `TENANTS` index. Each row has id, name, kind, createdAt, member count, the creator's email, `suspendedAt` and `deletingAt`. |
| `GET /api/operator/tenants/{tid}` | The tenant as above, its members (name, email, role, joinedAt) and its pending invite count. |
| `GET /api/operator/tenants/{tid}/audit?cursor=` | The tenant's audit, in the shape the Portal's own audit screen uses. |
| `POST /api/operator/tenants/{tid}/suspend` and `/unsuspend` | Sets or clears `suspendedAt`, writes the tenant audit, and emails the owners. |
| `POST /api/operator/tenants/{tid}/audit` | Body `{action: "tenant-quota", from, to}`. Writes the tenant audit only. |
| `GET /api/operator/users?email=` | Exact match through `EMAIL#`, or `404`. Returns id, email, display name, `emailVerifiedAt`, `suspendedAt`, factor count, `lastSignInAt`, and the user's tenants with role. |
| `POST /api/operator/users/{uid}/suspend` and `/unsuspend` | Suspend sets `suspendedAt` and bumps `sessionEpoch`, which signs the user out everywhere. Unsuspend clears it. Both email the user. |
| `POST /api/operator/users/{uid}/resend-verification` | Only for an unverified user; otherwise `409 already_verified`. |
| `POST /api/operator/users/{uid}/factor-reset` | Starts the lost-factor wait directly: the 24-hour wait and the `factor-reset-started` email, with no confirm link. The operator has already confirmed the person by other means. |

- The tenant audit names the actor "AuthGate administrator", never the operator. The entry
  carries the fixed actor id `operator`, and the Portal's audit screen shows that id in the
  reader's language.
- An operator route never returns a password hash, a recovery code, a token or a session.

### 4.2 A suspended tenant

- Every write in that workspace answers `409 tenant_suspended`:
  - clients, which AuthGate refuses already;
  - invites;
  - role changes and member removal;
  - renaming;
  - deleting the organisation;
  - accepting an invite to it.
- A member may still leave it.
- Account deletion that would delete a suspended tenant answers `409 tenant_suspended`. The
  tenant and its audit stay for the operator to inspect.
- The workspace shows a banner that points to the support address, and write controls are
  disabled. Reads keep working. `GET /api/config` gains `supportEmail` for the banner.

### 4.3 Data and email

- User: `lastSignInAt`, written on each successful sign-in.
- Four email kinds, in EN and VI, with fixed text and the support address. None carries a reason.
  - `tenant-suspended` and `tenant-unsuspended` go to the tenant's owners.
  - `account-suspended` and `account-unsuspended` go to the user.

## 5. The Console

### 5.1 Screens

- **Navigation:** Clients, Tenants, Users, Audit log, Admins, My account.
- **Tenants:** a table with name, kind, the creator's email, members, clients against quota, and
  status (active, suspended or deleting). It has a name filter over the loaded pages and Load more.
  On a phone each row becomes a card (the `data-label` contract from `feat/mobile`).
- **A tenant's page:**
  - **Header:** name and kind, plus two status badges: AuthGate (do its keys work) and Portal
    (is it read-only). When the two disagree, an "Out of sync" warning offers to run the missing
    step.
  - **Quota:** "3 of 5 clients", with a change action.
  - **Members:** read only.
  - **Clients:** from `?owner=`, each linking to the existing client page. That page already
    edits `rateLimit`.
  - **Audit:** the tenant's own entries, paged.
  - **Danger zone:**
    - Suspend opens a dialog with a required reason of 1 to 200 characters.
    - Unsuspend needs only step-up.
    - The last suspension's reason, admin and time are shown, from the Console audit.
- **Users:**
  - **Search:** one exact-email search box.
  - **Result:** a card with name, email, verified or not, status, factor count, last sign-in,
    and tenants with role, each linking to its page.
  - **Actions:**
    - suspend, which needs a reason, or unsuspend;
    - resend verification, shown only while unverified;
    - force factor reset, with a confirmation that names the 24-hour wait and the emails.
- **Clients:**
  - An owner filter: all, operator, or one tenant.
  - The owner cell links to the tenant's page.
- **My account:** an avatar, as the Portal has. PNG, JPEG or WebP up to 1 MB, with the same
  upload, confirm and serve path as the Portal. It is stored in a new private bucket,
  `auth-gate-console-<stage>-avatars-<account>`.

### 5.2 Rules

- Every write needs step-up. The step-up actions are:
  - on target `tid`: `tenant-suspend`, `tenant-unsuspend`, `tenant-quota`;
  - on target `uid`: `user-suspend`, `user-unsuspend`, `user-resend-verification`,
    `user-factor-reset`.
- Suspend and unsuspend of a tenant run in this order:
  1. AuthGate `POST /api/owners/{tid}/suspend` (or `/unsuspend`).
  2. Portal `POST /api/operator/tenants/{tid}/suspend` (or `/unsuspend`).
- A quota change runs AuthGate `PATCH /api/owners/{tid}` first, then the Portal's tenant audit.
- The Portal's own AuthGate client is protected like the Console's own client: rotate, disable,
  delete and purge answer `409 own_client_protected`. Its id reaches the Console as a stack
  parameter.
- Console audit actions:
  - `tenant-suspend`, which records a `reason` detail, and `tenant-unsuspend`, which records none;
  - `tenant-quota`, which records `from` and `to`;
  - `user-suspend`, which records a `reason` detail, and `user-unsuspend`, which records none;
  - `user-resend-verification` and `user-factor-reset`.
  - When the Portal step fails after AuthGate succeeded, the entry records `result: partial`.

## 6. Not in scope

- Browsing every user; the search is exact email only, as the parent spec says.
- Reading a password, a recovery code or a client key.
- Signing in as a user.
- Hard-deleting a tenant or a user.
- Bulk actions.
- The WAF stack, which is `feat/waf`.

## 7. Deployment

- **Before merge**, the owner runs `scripts/portal/operator-key.sh`. It creates the SSM
  SecureString from a key generated locally, never on argv, never printed. It does nothing if the
  parameter exists. CI fails the deploy if the parameter is missing.
- **Portal stack:** reads the operator key and needs no new IAM.
- **Console stack:**
  - Reads the operator key.
  - Takes two new parameters, `PortalUrl` and `PortalClientId`. `scripts/console/deploy.sh` fills
    them from the Portal stack's outputs.
  - Gains the avatar bucket. The deployer policy already holds `s3:*` on `auth-gate-console-*`
    and SSM rights on `/credo/auth-gate-portal/*`. The **permissions boundary** does not: its
    avatar rule covers only `auth-gate-portal-*`. `infra/role-boundary.json` gains the Console's
    `avatars/*` and `pending/*`, and **before merge** the owner reruns
    `scripts/setup-and-deploy/00-deployer.sh` under the admin profile to apply it.
- **Deploy order:** today CI deploys the Console before the Portal. This branch swaps them, so the
  routes exist when the Console calls them.
- The avatar code moves from `internal/portal/avatar` to `internal/platform/avatar`, because the
  Portal and the Console share code only through `internal/platform`.
- **Fixed monthly cost:** none.

## 8. Tests and checks

Go tests on DynamoDB Local, one for each failure that would do real harm:

- The operator routes refuse a missing or wrong key.
- A suspended tenant refuses each Portal write, and a member can still leave.
- A suspended user's existing session is refused on the next request.
- Suspending twice sends one email.
- Resend verification is refused for a verified user.
- The Console:
  - calls AuthGate then the Portal;
  - records `partial` when the Portal fails;
  - refuses a missing or over-long reason;
  - refuses every write without step-up;
  - protects the Portal's own client.

Web tests: the tenant page's "Out of sync" warning, and the suspend dialog, which cannot be sent
without a reason.

Bruno and smoke:
- `bruno-portal`: `/api/operator/*` without the key answers `401`.
- `bruno-console`: the new Console routes without a session answer `401`.
- `scripts/portal/smoke.sh`: checks the operator boundary.

The owner's round on stg uses one Portal account with a client, and the Console:
1. Suspend the tenant. Within 30 seconds the client's key answers `401`, the Portal is
   read-only, and the email arrives.
2. Unsuspend it.
3. Change the quota.
4. Suspend the user and confirm that their session ends, then unsuspend.
5. Force a factor reset.

## 9. Docs

- `docs/reference/portal.md`, `docs/reference/console.md` and `docs/reference/api.md` (the `?owner=` filter) gain the new routes.
- A new decision record, `docs/memo/operator-side.md`. It covers why the Console calls the Portal
  over HTTP with its own key, the AuthGate-first order, the internal reason, and what an operator
  can never do.
- The parent spec §10 gets a line saying that this document refines it.
