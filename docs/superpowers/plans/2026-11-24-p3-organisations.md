# Self-service P3: Organisations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Teams in the Portal. A person creates an organisation, invites people by email, manages their roles, and shares the organisation's clients. The organisation can be deleted, and an attempt that stops part way resumes.

**Architecture:**
- The Go Portal gains a store layer and HTTP routes for organisations, members and invites.
- Every limit is guarded by counters on the tenant item, checked inside the transaction that writes.
- Deletion runs inline and can be resumed.
- `portal-web` gains Members and Settings screens, a "New organisation" dialog, and an invite page that works signed out.
- Four new email kinds use the existing layout.

**Tech Stack:** Go and aws-sdk-go-v2 (DynamoDB transactions), tested against DynamoDB Local; React 19, Mantine 7 and vitest (`portal-web`); Bruno (`bruno-portal`).

**Spec:** `docs/superpowers/specs/2026-11-24-p3-organisations-design.md`. Its parent is `docs/superpowers/specs/2026-11-22-self-service-design.md`, sections 5, 6 and 9.

## Global Constraints

- Quotas:
  - 3 organisations created per person (`orgsCreated < 3`);
  - 20 members per organisation;
  - 20 pending invites per organisation;
  - 20 invites per day per organisation.
- An invite lives 7 days. Its token is 32 random bytes, and only its hex SHA-256 is stored. It travels in the link's fragment (`/invite/<tenantId>#token=<token>`) and in POST bodies, never in a URL path or query.
- Organisation names: 2 to 64 characters after trimming, no control characters, and `hasURL` false. Display names, from now on: 1 to 64 characters, no control characters, and `hasURL` false.
- Step-up guards role changes, member removal and organisation deletion. Leaving needs no step-up.
- Every org-only route (rename, delete, members, leave, invites) answers `400 personal_workspace` on a personal workspace.
- A tenant with `deletingAt` answers `404 not_found` on every `/api/t/{id}/…` route except `DELETE /api/t/{id}`.
- New error codes. Each one needs EN and VI strings in `portal-web` (`strings.test.ts` enforces it):
  - `personal_workspace` (400);
  - `org_quota_exceeded` (409);
  - `members_full` (409);
  - `invites_full` (409);
  - `last_owner` (409);
  - `already_member` (409);
  - `already_invited` (409);
  - `invite_not_found` (404);
  - `invite_wrong_email` (403);
  - `sole_owner` (409, with `organisations: [names]`);
  - `invalid_role` (400);
  - `name_mismatch` (400).
- English in code, docs and commits. Conventional Commits, ending `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Go store and HTTP tests skip without `DYNAMO_ENDPOINT`. Run them with `make test-store` (`docker compose up -d dynamodb`). Never call real AWS. The gate is `pnpm run check`.
- Tests only where they catch real harm. Each test named in a task has a reason in the spec's section 9 or in Review Focus.

## Review Focus

1. **An invited address in another case or with spaces** (`Ann@X.com ` against `ann@x.com`). It still matches, because both sides go through `NormalizeEmail`. Pinned in Tasks 2 and 5.
2. **An invite re-sent to an address whose previous invite expired but is not yet removed by TTL.** It succeeds and the count stays right. Pinned in Task 2.
3. **A member's open tab after an owner starts deleting the organisation.** Its next client create or list answers 404 and creates nothing. Pinned in Task 6.
4. **Deleting an account that is one of two owners of an organisation.** The organisation survives with one owner, `ownerCount` 1 and `memberCount` one less. Pinned in Task 6.
5. **A display name saved before P3 that contains a URL.** It never reaches an invite email: "A member of <org>" is used instead. Pinned in Task 3.

---

### Task 1: The organisation and member store

**Files:**
- Create: `internal/portal/orgs_store.go`, `internal/portal/orgs_store_test.go`

**Interfaces:**
- Produces:
  - Constants `maxOrgsCreated = 3`, `maxMembers = 20`.
  - Errors:
    - `ErrOrgQuota`;
    - `ErrLastOwner`;
    - `ErrPersonal` (the tenant is personal);
    - `ErrTenantDeleting`;
    - `ErrNotMember` (exists) and `ErrConflict` (exists) are reused.
  - `type Member struct { UserID, DisplayName, Email, Role string; JoinedAt time.Time }`
  - `type OrgCounts struct { Members, Owners, Invites int }`. `Tenant` gains a `Counts OrgCounts` field, filled by `GetTenant` from `memberCount`, `ownerCount` and `inviteCount`.
  - `func (st *Store) CreateOrg(ctx context.Context, u User, tenantID, name string, now time.Time) (Tenant, error)`
  - `func (st *Store) RenameOrg(ctx context.Context, tenantID, name string) error`
  - `func (st *Store) ListMembers(ctx context.Context, tenantID string) ([]Member, error)`: sorted by role (owner, admin, member), then by lower-cased display name. A membership whose user item is missing is skipped.
  - `func (st *Store) SetRole(ctx context.Context, tenantID string, actor Membership, userID, role string) (old string, err error)`
  - `func (st *Store) RemoveMember(ctx context.Context, tenantID string, actor Membership, userID string) (Membership, error)`. Leaving is `RemoveMember` with `actor.UserID == userID`.

**Rules:**
- **CreateOrg** is one transaction:
  - the user item gets `orgsCreated + 1`, on the condition `orgsCreated < :max`, else `ErrOrgQuota`. A missing attribute counts as 0, via `attribute_not_exists(orgsCreated) OR orgsCreated < :max`.
  - the tenant item is put with kind `org`, the name, `createdBy` and `createdAt`, and `memberCount` 1, `ownerCount` 1, `inviteCount` 0;
  - the `TENANTS` index item is put;
  - the membership pair is put with role `owner`.
- **RenameOrg:** the condition is `kind = org AND attribute_not_exists(deletingAt)`. It also updates the index item's name.
- **SetRole and RemoveMember** read the target's membership first, then write one transaction:
  - both membership items are updated (or, for a removal, deleted), on the condition `role = :old`;
  - the tenant item's counters are updated. `ownerCount` changes when the role moves into or out of `owner`, and losing an owner adds the condition `ownerCount > :one`. A removal also decrements `memberCount`. Every write adds `attribute_not_exists(deletingAt)`.
  - a ConditionCheck that the actor's membership still has `role = actor.Role`. It is **left out when `actor.UserID == userID`**, because DynamoDB refuses two operations on one item in a transaction. The update's own `role = :old` condition covers that case.
- On a failed condition, re-read the tenant:
  - `ErrTenantDeleting` if `deletingAt` is set;
  - `ErrLastOwner` if the change removes an owner and `ownerCount <= 1`;
  - `ErrConflict` otherwise.
- A personal tenant gives `ErrPersonal` from `SetRole`, `RemoveMember` and `RenameOrg`. Check `Kind` before writing.
- Who may do what (an admin cannot touch an owner) is the HTTP layer's job (Task 4). The store guarantees only the counters, the actor's role, and the existence of an owner.

- [ ] **Step 1: Write the failing tests** (DynamoDB Local; reuse `newEnv`/`Store` as `store_test.go` does):
  - `TestCreateOrgQuotaUnderRace`: one user, 4 goroutines each calling `CreateOrg`. Exactly 3 succeed, one gives `ErrOrgQuota`, and the user's `OrgsCreated` is 3.
  - `TestCreateOrgWritesOwner`: the membership pair has role owner, `GetTenant` gives `Counts{1,1,0}` and kind `org`, and `ListMemberships` of the user includes it.
  - `TestTwoOwnersDemotingEachOtherLeaveOne`: an org with owners A and B (B promoted via `SetRole`). Concurrently, A demotes B and B demotes A, each with its own `actor`. Exactly one succeeds, the other gives `ErrLastOwner` or `ErrConflict`, and in the end `ownerCount == 1` and exactly one membership has role owner.
  - `TestLastOwnerCannotLeaveOrStepDown`: the sole owner gets `ErrLastOwner` from `RemoveMember(self)` and from `SetRole(self, admin)`.
  - `TestRemoveMemberCounts`: removing a member leaves `Counts{1,1,0}`, and both items of the pair are gone.
  - `TestActorDemotedMidwayIsRefused`: actor A's membership is changed to admin directly, then `SetRole` with `actor.Role == owner` gives `ErrConflict`.
  - `TestPersonalRefused`: `SetRole` and `RenameOrg` on a personal tenant give `ErrPersonal`.
- [ ] **Step 2:** Run `make test-store` (or `DYNAMO_ENDPOINT=http://localhost:8000 go test ./internal/portal/ -run 'Org|Owner|Member|Personal'`). Expected: FAIL (undefined).
- [ ] **Step 3: Implement** in `orgs_store.go`, using `dynamo.TransactWrite` and `dynamo.IsConditionFailed` as `VerifyPending` does.
- [ ] **Step 4:** Run the tests. Expected: PASS. Run `go vet ./...`.
- [ ] **Step 5: Commit.** `feat(portal): organisations and members in the store, with counters and the last-owner guard`.

### Task 2: The invite store

**Files:**
- Create: `internal/portal/invites_store.go`, `internal/portal/invites_store_test.go`

**Interfaces:**
- Consumes `Tenant.Counts`, `ErrTenantDeleting` and `ErrPersonal` from Task 1.
- Produces:
  - Constants `maxPendingInvites = 20`, `inviteTTL = 7 * 24 * time.Hour`.
  - Errors `ErrInvitesFull`, `ErrMembersFull`, `ErrAlreadyInvited`, `ErrAlreadyMember`, `ErrInviteNotFound`.
  - `type Invite struct { ID, TenantID, Email, Role, InvitedBy, TokenHash string; CreatedAt, ExpiresAt time.Time }`. `Email` is normalised, and `ID` is a ULID.
  - `func (st *Store) CreateInvite(ctx context.Context, inv Invite, now time.Time) error`
  - `func (st *Store) ListInvites(ctx context.Context, tenantID string, now time.Time) ([]Invite, error)`, newest first.
  - `func (st *Store) ResendInvite(ctx context.Context, tenantID, inviteID, newHash string, now time.Time) (Invite, error)`
  - `func (st *Store) RevokeInvite(ctx context.Context, tenantID, inviteID string) (Invite, error)`
  - `func (st *Store) PeekInvite(ctx context.Context, tenantID, tokenHash string, now time.Time) (Invite, error)`
  - `func (st *Store) AcceptInvite(ctx context.Context, inv Invite, u User, now time.Time) error`
  - `func (st *Store) DeclineInvite(ctx context.Context, inv Invite) error`
  - `func (st *Store) DeleteAllInvites(ctx context.Context, tenantID string) error`, for Task 6.

**Rules:**
- **Items:**
  - `TENANT#<id>`/`INVITE#<tokenHash>` holds id, email, role, invitedBy, createdAt, expiresAt and `ttl`;
  - `TENANT#<id>`/`INVITEE#<email>` holds tokenHash, expiresAt and `ttl`.
- **The prune** (`pruneInvites`, called at the start of `CreateInvite` and `ListInvites`) queries `INVITE#`. For each expired invite it runs one transaction:
  - delete the invite;
  - delete its invitee item, on the condition `tokenHash = :h`, so a newer invite is never hit (if the condition fails, skip that item and do the other two);
  - `inviteCount - 1` on the condition `inviteCount > 0`.
- **CreateInvite** is one transaction:
  - put the invite;
  - put the invitee item, on the condition `attribute_not_exists(pk) OR expiresAt < :now`, else `ErrAlreadyInvited`;
  - the tenant item gets `inviteCount + 1`, on the condition `kind = org AND attribute_not_exists(deletingAt) AND inviteCount < :max`.

  A failure maps, by re-reading, to `ErrTenantDeleting`, `ErrPersonal`, `ErrInvitesFull` or `ErrAlreadyInvited`. An expired invitee item that was overwritten leaves its old invite to the prune, and the count stays right, because the prune runs first.
- **ResendInvite:** find the invite by `ID` (query `INVITE#`, at most 20). One transaction then deletes the old invite item, puts the new one (same ID, new hash, `expiresAt = now + 7 days`), and updates the invitee item's hash and expiry. The count is unchanged.
- **RevokeInvite and DeclineInvite:** delete the pair and decrement `inviteCount`.
- **PeekInvite:** a missing or expired item gives `ErrInviteNotFound`.
- **AcceptInvite** is one transaction:
  - delete the invite, on the condition `attribute_exists(pk) AND expiresAt > :now`;
  - delete the invitee item;
  - the tenant item gets `memberCount + 1, inviteCount - 1`, on the condition `memberCount < :max AND attribute_not_exists(deletingAt)`;
  - put the membership pair with the invite's role (`ownerCount + 1` when the role is owner), on the condition `attribute_not_exists(pk)`.

  `NormalizeEmail(u.Email)` must equal `inv.Email`, else `ErrInviteNotFound`; the store checks it too. Failures map to `ErrMembersFull`, `ErrTenantDeleting`, `ErrAlreadyMember` or `ErrInviteNotFound`.

- [ ] **Step 1: Write the failing tests:**
  - `TestInvitesCapUnderRace`: 21 concurrent `CreateInvite` to distinct addresses. 20 succeed, 1 gives `ErrInvitesFull`, and `inviteCount == 20`.
  - `TestAcceptIntoFullOrgRefused`: set `memberCount` to 20 directly. `AcceptInvite` gives `ErrMembersFull`, and the invite still exists.
  - `TestInviteOnlyForInvitedAddress`: a user with another email gets `ErrInviteNotFound`. A user `Ann@X.com` accepts the invite made for ` ann@x.com ` (Review Focus 1), and the membership pair has the invite's role.
  - `TestInviteTokenSingleUse`: after an accept, `PeekInvite` with the same hash gives `ErrInviteNotFound`.
  - `TestExpiredInviteRefusedAndFreed`: after `now + 7 days + 1s`, `PeekInvite` gives `ErrInviteNotFound`, `ListInvites` returns none, and `inviteCount == 0`.
  - `TestReinviteAfterExpiry`: an expired invite whose items are still present. A new `CreateInvite` to the same address succeeds and `inviteCount == 1` (Review Focus 2).
  - `TestResendKillsOldToken`: after `ResendInvite`, the old hash gives `ErrInviteNotFound` and the new one works.
  - `TestInviteDuplicateRefused`: a second live invite to the same address gives `ErrAlreadyInvited`.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement** in `invites_store.go`.
- [ ] **Step 4:** Run. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal): invites in the store, single use, counted and pruned`.

### Task 3: Names and the organisation emails

**Files:**
- Create: `internal/portal/names.go`, `internal/portal/names_test.go`
- Modify:
  - `internal/portal/signup.go`: `validName` also refuses `hasURL`;
  - `internal/portal/mail/mail.go` and `internal/portal/mail/templates.go`: four kinds, new vars;
  - `internal/portal/mail/mail_test.go`.

**Interfaces:**
- Produces:
  - `func hasURL(s string) bool` in package `portal`;
  - `func validOrgName(raw string) (string, bool)`: trimmed, 2 to 64 runes, no control characters, `!hasURL`;
  - `func inviterLabel(displayName string) string`: the name, or "" when `hasURL` is true or the name is empty. The mail layer then uses its fallback.
  - Mail kinds `org-invite` (a link kind), `org-removed`, `org-role-changed`, `org-deleted`, with the vars below. `mail.Kinds` lists them.

**hasURL** is case-insensitive and true when the value contains:
- `://`;
- `www.`;
- or a host-like token, `[a-z0-9-]+\.[a-z]{2,63}`, followed by end of string, whitespace, or one of `/ : ? #`.

Test rows:

| Value | `hasURL` |
|---|---|
| `Acme` | false |
| `Mr. Smith` | false |
| `J.R.R. Tolkien` | false |
| `Nguyễn Văn A` | false |
| `acme.io` | true |
| `see acme.io/x` | true |
| `http://x` | true |
| `WWW.EVIL` | true |
| `evil.com?a` | true |

**Mail vars:**
- `Message.Vars` may now also hold `org`, `inviter` and `role`, but only for the four org kinds. Any other kind refuses them, as unknown vars are refused today.
- Each value has at most 64 runes and no control characters. A value that fails `hasURL` is refused with an error, so a caller bug cannot mail a link.
- `role` must be `owner`, `admin` or `member`. It is rendered translated (VI: "chủ sở hữu", "quản trị viên", "thành viên").
- An empty `inviter` renders as "A member of {org}" / "Một thành viên của {org}".
- Update the `Message` doc comment: an organisation name and a display name are the only typed values, and they are checked.

**Templates** (the copy is fixed; the implementer writes the VI copy as natural Vietnamese):

| Kind | Subject (EN) | Body (EN) |
|---|---|---|
| `org-invite` | `You're invited to {org}` | `{inviter} invited you to join {org} on the AuthGate Portal as {role}. The invite expires in 7 days.` Button: `View the invite` / `Xem lời mời`. |
| `org-removed` | `You were removed from {org}` | `You are no longer a member of {org}. Its clients and audit log are closed to you.` |
| `org-role-changed` | `Your role in {org} changed` | `Your role in {org} is now {role}.` |
| `org-deleted` | `{org} was deleted` | `The organisation {org} and its clients were deleted.` |

Footers:
- `org-invite`: `If you do not expect this invite, ignore this email; nothing changes unless you accept it.`
- The three notices: `If you think this is a mistake, ask an owner of the organisation.` They keep the sign-in button.

- [ ] **Step 1: Write the failing tests:**
  - `TestHasURL` with the rows above.
  - `TestValidOrgName`: `"A"` false, `"Acme"` true, a 65-rune name false, `"acme.io"` false, `" Acme "` gives `"Acme"`.
  - In the sign-up tests, a display name `see acme.io` answers `400 invalid_name`.
  - In `mail_test.go`:
    - each org kind renders in EN and VI, with the org name in subject and body;
    - `org-invite` with an empty `inviter` says "A member of Acme" (Review Focus 5);
    - an `inviter` of `evil.com` is refused;
    - `org` on kind `verify` is refused;
    - `role` `root` is refused;
    - HTML-escaping holds for `<b>Acme</b>`.
- [ ] **Step 2:** Run `go test ./internal/portal/...`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal): no URLs in names, and the four organisation emails`.

### Task 4: Organisation and member routes

**Files:**
- Create: `internal/portal/orgs.go`, `internal/portal/orgs_test.go`
- Modify:
  - `internal/portal/member.go`: the guard reads the tenant;
  - `internal/portal/stepup.go`: new actions;
  - `internal/portal/app.go`: mount;
  - `internal/portal/tenants.go`: the `kind` in `tenantView` stays `org` (the web fix is in Task 8).

**Interfaces:**
- Consumes Tasks 1 and 3.
- Produces:
  - `func tenantFrom(ctx context.Context) Tenant`, set by the member guard.
  - `func (a *App) memberAny(allowDeleting bool, roles ...string) func(http.Handler) http.Handler`. `a.member(roles...)` becomes `a.memberAny(false, roles...)`.
  - Step-up actions `change-role` and `remove-member`, with target kind `memberTarget` (`<tenantId>/<userId>`, both ULIDs), and `delete-org` with target kind `tenantTarget` (a ULID).
  - Routes:
    - `POST /api/tenants`;
    - `PATCH /api/t/{tenantId}`;
    - `GET /api/t/{tenantId}/members`;
    - `PATCH /api/t/{tenantId}/members/{userId}`;
    - `DELETE /api/t/{tenantId}/members/{userId}`;
    - `POST /api/t/{tenantId}/leave`.

**Rules:**
- **The member guard** reads the tenant after the membership. Missing, or `DeletingAt` set and `!allowDeleting`, gives `404 not_found`. It stores the tenant in the context.
- **`POST /api/tenants` `{name}`:**
  - `validOrgName`, else `400 invalid_name`;
  - `ErrOrgQuota` gives `409 org_quota_exceeded`;
  - success is `201 {tenantId, kind: "org", name, role: "owner"}`;
  - audit `org-create` in the new tenant.
- **`PATCH /api/t/{id}` `{name}`:** owner only, audit `org-rename`, success `200`.
- **`GET …/members`:** any member. The answer is `{members: [{userId, displayName, email, role, joinedAt}]}`.
- **`PATCH …/members/{userId}` `{role}`:**
  - step-up `change-role` with target `<tenantId>/<userId>`;
  - a role other than owner, admin or member gives `400 invalid_role`;
  - an owner may set any role on anyone, themselves included (stepping down);
  - an admin may only move a non-owner between `admin` and `member`. Anything else, and any call by a member, gives `403 forbidden`;
  - `ErrLastOwner` gives `409 last_owner`;
  - the audit is `member-role-change`, with detail `from` and `to`;
  - the person changed is emailed `org-role-changed` (best effort), unless they are the actor.
- **`DELETE …/members/{userId}`:**
  - step-up `remove-member`;
  - an owner removes anyone but themselves (use leave for that). An admin removes admins and members. Anything else gives `403`;
  - audit `member-remove`, and mail `org-removed` to the removed person.
- **`POST …/leave`:** any member, no step-up. `409 last_owner` for the last owner. Audit `member-leave`. No email.
- **On a personal workspace:** `ErrPersonal` from any of these gives `400 personal_workspace`. Leave checks the tenant kind first.

- [ ] **Step 1: Write the failing tests** over HTTP with `newAuthEnv`, `e.owner`, `b.stepUp` and `stepHeader`. A helper `e.orgWith(t, owner *browser, members ...User)` creates the org through the API and adds members with `Store.CreateInvite` plus `AcceptInvite`:
  - `TestCreateOrgOverHTTP`: 201, and `GET /api/tenants` lists the personal workspace first, then the org with role owner. A fourth org gives `409 org_quota_exceeded`.
  - `TestMemberRoutesForbiddenToMembers`: a member gets `403` on `PATCH /api/t/{id}`, `PATCH …/members/{x}` and `DELETE …/members/{x}`.
  - `TestAdminCannotTouchOwner`: an admin's `PATCH` of an owner to member and `DELETE` of an owner both give `403`. An admin promoting a member to owner gives `403`.
  - `TestRemovedMemberRefusedNextRequest`: after removal, the removed member's `GET /api/t/{id}/clients` gives `404`, and an `org-removed` mail went to them.
  - `TestRoleChangeNeedsStepUp`: without the header, `403 step_up_required`. A token minted for another target gives `403`.
  - `TestPersonalWorkspaceRoutes`: `POST /api/t/{personal}/leave` and `GET …/members` give `400 personal_workspace`.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement.** Map the store errors in one helper, `orgErr(err error) error`, in `orgs.go`. Tasks 5 and 6 reuse it.
- [ ] **Step 4:** Run the whole `./internal/portal/...`. The existing member-guard tests must stay green. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal): create organisations and manage members over HTTP`.

### Task 5: Invite routes

**Files:**
- Create: `internal/portal/invites.go`, `internal/portal/invites_test.go`
- Modify: `internal/portal/app.go` (mount)

**Interfaces:**
- Consumes Tasks 2, 3 and 4 (`orgErr`, the member guard).
- Produces these routes:
  - `GET` and `POST /api/t/{tenantId}/invites`;
  - `POST /api/t/{tenantId}/invites/{inviteId}/resend`;
  - `DELETE /api/t/{tenantId}/invites/{inviteId}`;
  - `POST /api/invites/{tenantId}/lookup`, `POST /api/invites/{tenantId}/accept` and `POST /api/invites/{tenantId}/decline`, each with the body `{token}`.

**Rules:**
- **`GET …/invites`** (owner or admin) answers `{invites: [{inviteId, email, role, invitedBy: {userId, displayName}, expiresAt}]}`.
- **`POST …/invites` `{email, role}`** (owner or admin):
  - `parseEmail`, else `400 invalid_email`;
  - an admin inviting `owner`, or any member calling, gives `403`;
  - an address that belongs to a member (`GetUserByEmail`, then `GetMembership`) gives `409 already_member`;
  - the limit bucket `invite#<tenantId>` uses `ratelimit.Settings{Burst: 20, PerSecond: 20.0 / 86400}` and is spent only after validation;
  - the token is 32 random bytes, hex SHA-256 stored, as `PutToken`'s callers do;
  - the mail is `org-invite` with link `PublicBaseURL + "/invite/" + tenantId + "#token=" + token`, `org`, `inviter` (from `inviterLabel`) and `role`. The language is the inviter's `Lang`;
  - success is `201 {inviteId, email, role, expiresAt}`, with audit `invite-create`, target the email.
- **`resend`:** a new token and a new mail, audit `invite-resend`. It uses the same limit bucket.
- **`DELETE`:** revoke, audit `invite-revoke`.
- **`lookup`:**
  - no session needed;
  - per-IP limit via `spendIP(r, "invite-lookup", loginIPLimit)`, after the body decodes;
  - the answer is `200 {orgName, inviterName, role, email}`, plus `emailMatches` when a full session exists. Read that session without requiring one: reuse the guard's optional read if one exists, otherwise read the cookie through `a.Store.GetSession` and check the epoch as the guard does;
  - an unknown, expired or malformed token, a bad tenant id, or a tenant with `deletingAt` gives `404 invite_not_found`.
- **`accept` and `decline`:**
  - a full session is needed;
  - the session's address must equal the invite's after `NormalizeEmail`, else `403 invite_wrong_email`;
  - accept audits `invite-accept` in the org and answers `200 {tenantId}`;
  - decline audits `invite-decline`;
  - the store errors map through `orgErr`.

- [ ] **Step 1: Write the failing tests:**
  - `TestInviteRoundOverHTTP`:
    1. the owner invites `bob@x.com` as member, and a mail of kind `org-invite` went to bob with a link of the form `https://portal.test/invite/<tid>#token=…`;
    2. a signed-out lookup with that token gives bob's address, the org name and the role;
    3. bob, signed in with `Bob@X.com`, gets `emailMatches: true`, accepts, and his `GET /api/tenants` lists the org as member;
    4. a second accept gives `404 invite_not_found`.
  - `TestInviteWrongEmail`: carol, signed in, gets `403 invite_wrong_email` and the invite survives.
  - `TestAdminCannotInviteOwner`: `403`.
  - `TestInviteAlreadyMember`: `409 already_member`.
  - `TestInviteDailyLimit`: the 21st invite in a day gives `429 rate_limited`, even after revoking some.
  - `TestLookupDeletingTenant`: a lookup on a tenant with `deletingAt` gives `404 invite_not_found`.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `./internal/portal/...`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal): invite, resend, revoke, look up, accept and decline`.

### Task 6: Deleting an organisation, and account deletion

**Files:**
- Create: `internal/portal/orgdelete.go`, `internal/portal/orgdelete_test.go`
- Modify:
  - `internal/portal/orgs_store.go`: deletion store methods;
  - `internal/portal/tenants.go`: resume from the list;
  - `internal/portal/profile.go`: `deleteAccount`.

**Interfaces:**
- Consumes Tasks 1 to 5, plus `a.workspaceClients(ctx, tenantID)` and `a.AuthGate.Tenant(id).Delete` (`profile.go`).
- Produces:
  - `func (st *Store) MarkDeleting(ctx context.Context, tenantID string, now time.Time) (Tenant, error)`: sets `deletingAt` if absent (kind `org` only, else `ErrPersonal`) and returns the tenant either way.
  - `func (st *Store) DropMembership(ctx context.Context, tenantID, userID string) error`: deletes the pair without counter conditions. Use it only during deletion.
  - `func (st *Store) FinishDeletion(ctx context.Context, t Tenant, now time.Time) error`. It does three things:
    - decrements the creator's `orgsCreated` once, guarded by a flag `creatorReleased` on the tenant item, written in the same transaction (`attribute_not_exists(creatorReleased)`). If the creator's user item is gone, it only sets the flag;
    - deletes the `TENANTS` index item;
    - sets `ttl = now + 30 days` on the tenant item and on every `AUDIT#` item of the tenant.
  - `func (a *App) finishOrgDeletion(ctx context.Context, t Tenant, actorID string) error`: steps 2 to 7 of spec section 4, in order. Each step skips what is already gone. It sends `org-deleted` to each member it drops (best effort), and drops the actor's membership last.
  - Route `DELETE /api/t/{tenantId}` `{name}`. It uses `memberAny(true, RoleOwner)`, step-up `delete-org` with target the tenant id, and `name_mismatch` unless `strings.TrimSpace(in.Name) == t.Name`, compared case-sensitively. A personal tenant gives `400 personal_workspace`. The audit is `org-delete`, written before the members are dropped. Success is `204`. An AuthGate failure gives `502 authgate_unavailable`, with `deletingAt` kept.

**Resume:**
- `listTenants` skips a tenant with `DeletingAt`, as now (nothing listed).
- It also calls `a.finishOrgDeletion(ctx, t, u.ID)` for such a tenant, logging a failure rather than returning it.
- A second `DELETE` resumes too: `MarkDeleting` returns the tenant already marked, and the step-up is still required.

**Account deletion** (`deleteAccount`). Before the step-up is spent, classify every org membership:
- role owner, `Counts.Owners == 1` and `Counts.Members > 1`: collect the org's name. If any are collected, answer `409 {error: "sole_owner", organisations: [names]}`;
- role owner, `Counts.Members == 1`: delete the org;
- anything else: leave it.

After the step-up is spent, in this order:
1. the personal clients (as now);
2. `MarkDeleting` and `finishOrgDeletion` for the orgs to delete;
3. `RemoveMember(self)` for the orgs to leave. `ErrLastOwner` or `ErrConflict` here gives `409 conflict`, and the person may retry;
4. factors and `Store.DeleteAccount` (as now).

- [ ] **Step 1: Write the failing tests** (`withPortalAPI` fakes AuthGate's portal routes; its `reset(status, body)` makes a call fail):
  - `TestDeleteOrgRound`: an org with 2 clients (the fake lists them), one member and one invite. `DELETE` with step-up and the exact name gives 204. AuthGate saw two `DELETE /admin/api/portal/clients/<id>` with `x-tenant-id` set to the org. Neither person's `GET /api/tenants` lists the org. The member got `org-deleted`. The creator's `OrgsCreated` is back to 0. Every remaining `TENANT#<id>` item has a `ttl` within 30 days.
  - `TestDeleteOrgResumes`: the fake fails the second client delete with 500, and the call gives 502. A member's next `GET /api/tenants` finishes the job: no items without `ttl` remain, and `OrgsCreated` is decremented exactly once. A second `DELETE` afterwards also succeeds or answers 404, with no double decrement.
  - `TestDeletingTenantRefusesWork`: after `MarkDeleting`, a member's `POST /api/t/{id}/clients` and `GET …/members` give `404`, and AuthGate saw no create (Review Focus 3).
  - `TestDeleteOrgNameMismatch`: `"acme"` against `"Acme"` gives `400 name_mismatch`, and nothing is marked.
  - `TestDeleteAccountSoleOwner`: the sole owner of an org with another member gets `409 sole_owner` with the org's name, and the step-up token stays unspent (a retry with it, after transferring ownership, works).
  - `TestDeleteAccountCoOwner`: one of two owners deletes their account. The org remains with `Counts{Members: n-1, Owners: 1}` (Review Focus 4).
  - `TestDeleteAccountOnlyMember`: the person's org, with no other members, is deleted with its clients.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `./internal/portal/...`. The existing `TestDeleteAccountDeletesWorkspaceClients` stays green. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal): delete an organisation, resumable; account deletion respects organisations`.

### Task 7: Close the sign-in address oracle

**Files:**
- Modify:
  - `internal/portal/auth.go` (`login`) and `internal/portal/auth_test.go`;
  - `portal-web/src/screens/SignIn.tsx`;
  - `portal-web/src/strings.ts`.

**Rules:**
- In `login`, an account with `EmailVerifiedAt == nil` is treated like a wrong password on a live account:
  - audit reason `unverified`;
  - `a.failSignIn(ctx, u, ip, acct)`;
  - `errInvalidCredentials`.

  Remove the `403 email_unverified` branch. `login` was the only sender of `email_unverified`, so remove the code from `portal-web/src/lib/api.ts` and its strings too.
- `SignIn.tsx` drops its `unverified` state. It always shows, under the form, a collapsed "Didn't get the verification email?" / "Chưa nhận được email xác minh?" section that opens the existing `<Resend initialEmail={email} />` component (today shown only on `email_unverified`).

- [ ] **Step 1: Write the failing test.** `TestLoginUnverifiedLooksLikeWrongPassword`: a pending account with the right password, and a verified account with a wrong password. Both give the same status and byte-identical body, and both increment the account's failure count. Replace any existing test that asserted `email_unverified` from login, and say so in the commit body (the requirement changed: spec section 6).
- [ ] **Step 2:** Run it. Expected: FAIL.
- [ ] **Step 3: Implement** Go and web.
- [ ] **Step 4:** Run `go test ./internal/portal/...` and `pnpm --filter portal-web test`. Expected: PASS.
- [ ] **Step 5: Commit.** `fix(portal): sign-in no longer tells an unverified address from a taken one`.

### Task 8: Web: organisations, members and settings

**Files:**
- Create:
  - `portal-web/src/lib/orgs.ts`;
  - `portal-web/src/screens/Members.tsx`;
  - `portal-web/src/screens/OrgSettings.tsx`;
  - `portal-web/src/screens/NewOrg.tsx` (the dialog);
  - `portal-web/test/members.test.tsx`.
- Modify:
  - `portal-web/src/lib/tenants.ts`: `kind: 'personal' | 'org'`, to fix the mismatch with the backend; add `membersPath` and `settingsPath`;
  - `portal-web/src/shell.tsx`: nav items and the switcher entry;
  - `portal-web/src/routes.tsx`;
  - `portal-web/src/stepup.tsx`: add `'change-role' | 'remove-member' | 'delete-org'`;
  - `portal-web/src/lib/api.ts`: the new error codes of Global Constraints;
  - `portal-web/src/strings.ts`: EN and VI.

**Interfaces:**
- Consumes the routes of Tasks 4, 5 and 6.
- Produces in `lib/orgs.ts`:
  - `type Member = { userId: string; displayName: string; email: string; role: Role; joinedAt: string }`;
  - `type PendingInvite = { inviteId: string; email: string; role: Role; invitedBy: { userId: string; displayName: string }; expiresAt: string }`;
  - `can(role: Role, action: 'invite' | 'change-role' | 'remove' | 'rename' | 'delete', target?: Role): boolean`, the table of spec sections 3 and 8 as a pure function that the screens use to show or hide controls;
  - `roleChoices(actor: Role, target: Role): Role[]`.

**Rules:**
- **Nav:**
  - Clients;
  - Members (org only);
  - Audit (owner or admin, as now);
  - Settings (org only);
  - Account.

  Switching workspaces keeps the section when the next workspace has it, else it opens Clients.
- **Switcher:** the last option is "+ New organisation" / "+ Tổ chức mới". It opens the `NewOrg` dialog (name field, `org_quota_exceeded` explained). On success it reloads the tenant list and navigates to the new org's Members page.
- **Members:**
  - a table of name, email, role and joined date;
  - the role is a Select where `roleChoices` is non-empty; changing it runs `stepUp('change-role', …, `${tenantId}/${userId}`)`;
  - Remove where `can(actor, 'remove', target)` allows, behind `stepUp('remove-member', …)`;
  - for owners and admins: an Invite button (dialog with email and role, limited to the actor's allowed roles), and "Pending invites (n/20)" with expiry, Resend and Revoke.
- **Settings:**
  - Rename, for the owner;
  - Leave, for everyone; after success, navigate to the personal workspace;
  - a danger zone, for the owner: Delete, which needs the name typed exactly; the button stays disabled until it matches, then `stepUp('delete-org', …, tenantId)`; after success, navigate to the personal workspace.
- The operator lock on clients already exists (`lockedByOperator`). Do not change it.

- [ ] **Step 1: Write the failing tests** in `members.test.tsx`, in the style of `workspace.test.tsx` (stubbed fetch):
  - `can` and `roleChoices` table tests:
    - a member can do nothing;
    - an admin on an owner gets `[]`;
    - an admin on a member gets `['admin','member']`;
    - an owner gets all three.
  - Members rendered as `member`: no Invite button, no role Select, no Remove.
  - Members rendered as `admin`: the owner's row has no Select or Remove, a member's row has both, and the Invite dialog offers no `owner`.
  - The shell for a personal tenant shows no Members or Settings link; for an org member it shows both, but not Audit.
  - Settings: Delete stays disabled until the exact name is typed.
- [ ] **Step 2:** Run `pnpm --filter portal-web test`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `pnpm --filter portal-web test`, `pnpm run typecheck` and `pnpm run lint`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal-web): organisations, members, invites and settings`.

### Task 9: Web: the invite page

**Files:**
- Create:
  - `portal-web/src/screens/Invite.tsx`;
  - `portal-web/src/lib/invite.ts`;
  - `portal-web/test/invite.test.tsx`.
- Modify:
  - `portal-web/src/routes.tsx`: `/invite/:tenantId`, outside `Frame`, so it renders signed out;
  - `Home` in `routes.tsx`;
  - `portal-web/src/screens/SignUp.tsx`: prefill;
  - `portal-web/src/strings.ts`.

**Interfaces:**
- Produces in `lib/invite.ts` (sessionStorage key `portal.invite`, wrapped in try/catch):
  - `rememberInvite(v: { tenantId: string; token: string; email: string }): void`;
  - `pendingInvite(): { tenantId: string; token: string; email: string } | null`, which returns null unless `tenantId` is a ULID;
  - `forgetInvite(): void`.

**Rules:**
- **On mount,** `Invite` takes the token with `takeFragmentToken()`. Without one, it falls back to `pendingInvite()` for the same tenant. It then posts `lookup`.
  - `404 invite_not_found`: "This invite is no longer valid", then `forgetInvite()`.
- **Signed out:**
  - it shows the org, the inviter, the role and the invited address;
  - it has two buttons, "Sign in" and "Sign up";
  - both call `rememberInvite` first, then navigate to `/sign-in` or `/sign-up`.
- **Signed in with `emailMatches: false`:** "This invite is for <email>. You are signed in as <your email>." No Accept button.
- **Signed in and matching:** Accept and Decline.
  - Accept: `forgetInvite()`, then navigate to `clientsPath(tenantId)`, and refresh the tenant list;
  - Decline: `forgetInvite()`, then navigate to `/`.
- **Home:** if `pendingInvite()` is set, navigate to `/invite/<tenantId>` instead of the personal clients. That covers every flow that ends on `/`: sign-in, second factor, enroll, recovery codes and verify.
- **SignUp:** the email field starts as `pendingInvite()?.email ?? ''`.

- [ ] **Step 1: Write the failing tests** (`invite.test.tsx`, stubbed fetch, `MemoryRouter`):
  - signed out, lookup ok: the invited address and both buttons show, and clicking "Sign up" stores the invite;
  - signed in with `emailMatches: false`: the wrong-address text shows and there is no Accept button;
  - Accept posts `{token}` to `/api/invites/<tid>/accept` and lands on `/t/<tid>/clients`;
  - `pendingInvite()` returns null for a stored tenantId of `../x`.
- [ ] **Step 2:** Run. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `pnpm --filter portal-web test` and `pnpm run typecheck`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(portal-web): the invite page, signed in or out`.

### Task 10: Boundaries, docs and the gate

**Files:**
- Create: `bruno-portal/01-boundary/07-…` to `12-…`
- Create: `docs/memo/portal-organisations.md`
- Modify:
  - `docs/superpowers/specs/2026-11-22-self-service-design.md`: sections 7 and 8;
  - `docs/reference/portal.md`;
  - `README.md` (one line in the Portal section).

**Bruno cases** (credential-free, in the style of `06-tenant-clients-without-cookie.yml`, with `docs:` saying why):

| File | Request | Expected |
|---|---|---|
| `07-create-org-without-cookie.yml` | `POST /api/tenants` `{"name":"Acme"}` | `401 {"error":"unauthorized"}` |
| `08-members-without-cookie.yml` | `GET /api/t/01HZZZZZZZZZZZZZZZZZZZZZZZ/members` | `401` |
| `09-invite-without-cookie.yml` | `POST /api/t/01HZZZZZZZZZZZZZZZZZZZZZZZ/invites` `{"email":"a@b.co","role":"member"}` | `401` |
| `10-accept-without-cookie.yml` | `POST /api/invites/01HZZZZZZZZZZZZZZZZZZZZZZZ/accept` `{"token":"x"}` | `401` |
| `11-lookup-junk-token.yml` | `POST /api/invites/01HZZZZZZZZZZZZZZZZZZZZZZZ/lookup` `{"token":"x"}` | `404 {"error":"invite_not_found"}` |
| `12-delete-org-without-cookie.yml` | `DELETE /api/t/01HZZZZZZZZZZZZZZZZZZZZZZZ` `{"name":"x"}` | `401` |

**Docs:**
- **Parent spec section 7**, three corrections and a note:
  - sign-up with a known address answers "check your email" and mails the owner;
  - verifying the email needs the password;
  - avatars upload under `pending/` first, with a 1-day expiry;
  - the squatting note: the newest sign-up replaces a pending one, so a squatter cannot hold an address.

  Section 8 gains a line at the top: refined by `2026-11-24-p3-organisations-design.md`.
- **`docs/memo/portal-organisations.md`:** a decision record in the style of `console-sessions.md`. It covers:
  - counters as the race guard, and why not counting queries;
  - the last-owner transaction, and why the self case drops the ConditionCheck;
  - inline, resumable deletion with no scheduler;
  - the oracle fix;
  - why the invite lookup works signed out.
- **`docs/reference/portal.md`:** the routes of Tasks 4 to 6 and the new error codes.

- [ ] **Step 1:** Write the bruno cases. Check them against `make portal-dev` locally, if it runs in this environment, with `bru run 01-boundary --env-var baseUrl=http://localhost:<port>`. If no local Portal runs, say so in the report.
- [ ] **Step 2:** Write the docs.
- [ ] **Step 3: Gate.** Run `make test-store`, then `pnpm run check`. Expected: both green.
- [ ] **Step 4: Commit.** `docs(portal): organisations memo and reference, spec section 7 corrected; bruno boundaries`.

## Rollout (owner, after merge)

- CI deploys the Portal on merge. Then:
  - run `scripts/portal/smoke.sh`, which now includes bruno cases 07 to 12;
  - the browser round from spec section 1, with two real accounts.
- There is no new infrastructure, IAM, SSM parameter or fixed cost.
