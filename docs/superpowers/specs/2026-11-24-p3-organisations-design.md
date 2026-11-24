# Self-service P3: organisations

Date: 2026-11-24. Status: approved by the owner in chat, section by section, the same day.

This refines section 8 of `2026-11-22-self-service-design.md` (the parent spec) into a buildable
design. Where the two differ, this file wins for P3. Sections 5, 6 and 9 of the parent still apply.

## 1. Purpose

P2 shipped accounts and a personal workspace for each person. P3 lets people work as a team:
they create an organisation, invite others by email, give them roles, and manage the organisation's
clients together.

Decided by the owner (not re-askable):

- Fix the sign-in address oracle carried over from P2 (section 6). The owner chose "Bịt lại"
  ("close it").
- Email the people whose rights change: on removal, a role change or promotion to owner, and when an
  organisation is deleted. The owner chose "Sự kiện ảnh hưởng quyền" ("events that change rights").
- Each workspace gets tabs: Clients, Members, Audit, Settings. The owner chose "Tab theo workspace".

**Done when:**

- Two real accounts on stg complete the round:
  1. one creates an organisation and invites the other;
  2. the other accepts and creates a client;
  3. the owner changes the other's role, then removes them, and the removed person's next request is
     refused;
  4. the owner deletes the organisation.
- The race, invite and role tests of section 9 pass, including the full organisation round over HTTP
  against DynamoDB Local. The Portal smoke covers the new routes' anonymous refusals.
- `pnpm run check` is green.

## 2. Data

These items are added to the parent's section 5, in the same table, `auth-gate-portal-<stage>`.

| Item | Key | Holds |
|---|---|---|
| Tenant (org) | `TENANT#<id>` / `META` | as today, plus `memberCount`, `ownerCount`, `inviteCount`, `deletingAt` |
| Invite | `TENANT#<id>` / `INVITE#<tokenHash>` | email, role, invitedBy, createdAt, expiresAt (7 days) |
| Invitee index | `TENANT#<id>` / `INVITEE#<normalised email>` | the invite's token hash and expiresAt. One pending invite per address per organisation. |

Notes:

- Every limit is a condition on the tenant item's counters, in the same transaction as the write it
  guards. There are three: 20 members, 20 pending invites, and at least one owner. Two concurrent
  writes therefore cannot pass a limit.
- Expired invites have no TTL, because TTL would delete them without decrementing `inviteCount`.
  Listing or creating invites removes the expired ones it finds and decrements the counter in the
  same transaction.
- The invite link is `<site>/invite/<tenantId>#token=<token>`. Like every emailed link in P2, the
  token sits in the fragment, so it never reaches CloudFront or a log, and the API takes it in a
  POST body. The token is 32 random bytes, and only its SHA-256 is stored.
- A personal workspace has no counters. Every organisation-only route below (rename, delete, members,
  leave, invites) answers `400 personal_workspace` on it (parent section 6).

## 3. API

Every route below needs a session in stage `full`, except the invite lookup. Routes under `/api/t/{tenantId}/` go through the
existing `member(roles…)` guard. A tenant with `deletingAt` answers `404 not_found` on every route
except its own `DELETE`.

| Route | Who | Rules |
|---|---|---|
| `POST /api/tenants` `{name}` | anyone | Name: 2 to 64 characters, no control characters, no URL. Condition `orgsCreated < 3` on the user. The creator becomes owner. |
| `PATCH /api/t/{id}` `{name}` | owner | Organisations only. Same name rules. |
| `DELETE /api/t/{id}` `{name}` | owner | Step-up. `name` must equal the current name. See section 4. |
| `GET /api/t/{id}/members` | any member | Display name, email, role, joinedAt. Members see each other. |
| `PATCH /api/t/{id}/members/{userId}` `{role}` | owner, admin | Step-up. An owner sets any role. An admin moves people between `admin` and `member` only, and cannot touch an owner. Promoting to `owner` is the ownership transfer. An owner may demote themselves ("step down") while another owner remains. |
| `DELETE /api/t/{id}/members/{userId}` | owner, admin | Step-up. An owner removes anyone else. An admin removes admins and members. Never the last owner. |
| `POST /api/t/{id}/leave` | any member | No step-up. The last owner cannot leave. A personal workspace cannot be left. |
| `GET /api/t/{id}/invites` | owner, admin | Pending invites, with email, role, inviter and expiry. |
| `POST /api/t/{id}/invites` `{email, role}` | owner, admin | An admin invites `admin` or `member`. Only an owner invites `owner`. `409 already_member` if the address belongs to a member. `409 already_invited` if one is pending; use resend instead. Rate limit 20 per day per organisation. |
| `POST /api/t/{id}/invites/{inviteId}/resend` | owner, admin | A new token replaces the old one and the expiry restarts. The old link stops working. |
| `DELETE /api/t/{id}/invites/{inviteId}` | owner, admin | Revoke. |
| `POST /api/invites/{tenantId}/lookup` `{token}` | whoever holds the token | No session needed, so the signed-out page can prefill sign-up. It returns the organisation name, the inviter's name, the role and the invited address. The holder received that address's email, so nothing new is revealed. With a full session it also returns `emailMatches`. Per-IP limit as for sign-in. An unknown, expired or used token answers `404 invite_not_found`. |
| `POST /api/invites/{tenantId}/accept` `{token}` | the invitee | The session's email must equal the invited address, else `403 invite_wrong_email`. Writes the membership pair, deletes the invite and its index item, and increments `memberCount` under `memberCount < 20` and no `deletingAt`, in one transaction. |
| `POST /api/invites/{tenantId}/decline` `{token}` | the invitee | Same email rule. Deletes the invite. |

`inviteId` is an opaque id stored on the invite, not the token hash, so the invite list never holds
anything that opens the link.

**The last-owner guard.** Demoting, removing or letting an owner leave runs as one transaction with
three parts:

- the membership change;
- the condition `ownerCount > 1` on the tenant item;
- a ConditionCheck that the actor is still an owner.

This is the Console's master guard (docs/memo/console-sessions.md). Two owners demoting each other
at once never leave none.

Every change to a role or a membership updates the pair `TENANT#/MEMBER#` and `USER#/TENANT#`
together. A removed member is refused by the next request, because the guard reads the membership
on every request with a consistent read.

## 4. Deleting an organisation

`DELETE /api/t/{id}`, by an owner, with step-up and the name typed again:

1. Set `deletingAt` on the tenant item, unless it is already set. From now on the tenant answers 404
   and is left out of every workspace list.
2. Delete each of the tenant's clients through the portal key (`DELETE /api/portal/clients/{id}`).
   A client already gone (404) is skipped.
3. Delete the invites and their index items.
4. Delete the other members' memberships, mailing each one `org-deleted` as theirs is dropped.
5. The store finish (`FinishDeletion`): decrement `orgsCreated` on the creator, if that account
   still exists, delete the tenant's index item in the `TENANTS` index, and set a 30-day TTL on the
   tenant's remaining items (the tenant item and its audit).
6. Delete the acting owner's membership last, and mail them `org-deleted`. It comes after step 5,
   not before it: if it went first and step 5 then failed, the actor could no longer run the
   deletion again, and no other member is guaranteed to exist. Kept until the end, it is the
   handle a resume holds.

Every step can be run again safely. A run that stops part way, through a timeout or an AuthGate
error, answers `502` and is resumed in either of two ways:

- the next `DELETE` on the same tenant;
- the next workspace list (`GET /api/tenants`) by any remaining member, which finishes the deletion
  of any tenant it finds with `deletingAt`.

There is no scheduler and no queue, so this adds no infrastructure and no fixed cost.

A deleting organisation answers 404 through the member guard on every route but its own `DELETE`. A
deleting personal workspace keeps P2's `409 tenant_deleting`.

**Known residual.** A client create that passed the guard just before `deletingAt` was set can
reach AuthGate after step 2 listed the clients. That client is then left behind at AuthGate, in a
tenant nobody can reach through the Portal. It is accepted: the window is the length of one request, and
the orphan holds no access the Portal can grant.

**Account deletion** (P2's `DELETE /api/me`) now considers each organisation the person belongs to,
before the step-up is spent:

- another owner remains: the person's membership is removed;
- the person is the only owner and other members remain: refused with `409 sole_owner`, listing
  those organisations by name, so the person can transfer ownership first;
- the person is the only member: the organisation is deleted as above. It is marked deleting only
  while it still has that one member (`MarkDeletingIfAlone`). If someone joined in the meantime,
  the answer is `409 sole_owner` with that organisation's name. The `org-delete` audit entry is
  written in each organisation the account deletion deletes, as it is for a direct delete.

## 5. Accepting an invite

- `/invite/<tenantId>#token=<token>` needs a `full` session. A visitor who is not signed in sees two
  buttons:
  - "Sign in";
  - "Sign up", with the invited email prefilled (the person may change it, but then the invite will
    not match).
- The Portal keeps the invite path as the return target. After sign-in, or after sign-up,
  verification and the first factor, it returns there.
- The page shows the organisation, the inviter and the role, with Accept and Decline. If the session's
  email is not the invited one, it says the invite is for another address and offers no button.
- Accept goes to `/t/<tenantId>/clients`.
- The parent spec's sign-up stays enumeration-free. Nothing on this page says whether the invited
  address has an account.

## 6. The sign-in address oracle (from P2)

**Today:** an attacker can sign up with a victim's address X and a password P of their own, then
sign in with X and P.

- `403 email_unverified` means X had no verified account: the pending sign-up took the address.
- `401` means a verified account holds X.

**The fix:**

- Sign-in with the right password on an unverified account answers the same `401 invalid_credentials`
  as any other failure. It counts as a failure for lockout and for the Turnstile threshold, so its
  side effects and timing match.
- The sign-in screen always shows "Didn't get the verification email? Resend". The resend route
  already answers the same whether or not the address exists.

## 7. Email

Four new kinds, in EN and VI, in the existing layout (`internal/portal/mail`):

| Kind | To | Says |
|---|---|---|
| `org-invite` | the invited address | `<inviter> invited you to <organisation> as <role>`, with the link, which expires in 7 days |
| `org-removed` | the removed member | you were removed from `<organisation>` |
| `org-role-changed` | the member | your role in `<organisation>` is now `<role>` (this covers promotion to owner) |
| `org-deleted` | every former member | `<organisation>` was deleted with its clients |

Notes:

- Only fixed text plus an organisation name and a display name (parent section 9). Both are plain
  text of at most 64 characters.
- **A gap found in design:** P2's `validName` refuses control characters but not URLs, so a display
  name can carry a link. The fix:
  - one shared `hasURL` check applies when an organisation name or a display name is written;
  - an existing display name that fails it is replaced in the email by "A member of `<organisation>`".
- A person leaving on their own, an invite being accepted or declined, and a rename send no email.
  They are recorded in the audit log.

## 8. The web app

- The workspace switcher gains "+ New organisation", a dialog with the name field, and the quota
  message `orgs_quota` when the person has created 3.
- Each workspace has tabs: Clients, Members, Audit, Settings. Which tabs show:
  - a personal workspace shows Clients and Audit;
  - an organisation's member sees Clients and Members;
  - owners and admins also see Audit;
  - every member sees Settings, where owners rename and delete and everyone else only leaves.
- **Members:**
  - the member table, with a role selector and Remove where the viewer's role allows them;
  - the Invite dialog (email and role);
  - the pending invites (n/20), each with its expiry, Resend and Revoke.

  Role changes and removals open the existing step-up dialog.
- **Settings:**
  - Rename;
  - Leave;
  - a danger zone with Delete, which needs the name typed and step-up.
- **The invite page,** as in section 5.
- **Clients** an operator disabled show "Locked by an administrator" and no Enable button.
- Every new string has EN and VI versions in the typed strings files.

## 9. Tests

Go tests against DynamoDB Local, each for a failure that would do real harm:

- **Races:**
  - four concurrent creates by one person end with three organisations;
  - 21 concurrent invites end with 20;
  - accepting into an organisation that has 20 members is refused;
  - two owners demoting each other at once leave one owner.
- **Invites:**
  - accepted only by the invited address;
  - a token works once;
  - an expired token is refused;
  - a resend kills the old link;
  - an admin cannot invite an owner;
  - expired invites free their slot in `inviteCount`.
- **Roles:**
  - an admin cannot change or remove an owner;
  - a member gets `403` on every owner and admin route;
  - a removed member is refused on the next request.
- **Deletion:**
  - a delete that fails after its second client resumes on the next `DELETE`, and also from a
    member's workspace list, and ends with nothing left but TTL'd items;
  - account deletion answers `sole_owner` where the rule says so, and deletes an organisation whose
    only member is the person.
- **Oracle:** sign-in to an unverified account with the right password answers the same status and
  body as a wrong password.
- **Email:** a display name containing a URL never reaches an email.

Also:

- web tests for the invite page states (signed out, wrong email, accept) and for which Members and
  Settings controls each role sees;
- `bruno-portal` gains the organisation boundary cases;
- `scripts/portal/smoke.sh` stays credential-free (it cannot sign up: Turnstile and email stand in
  the way). Through `bruno-portal`, it proves that every new route refuses an anonymous caller. The
  organisation round itself is proven by the Go HTTP tests and by the owner's two-account browser run.

## 10. Docs

- In the parent spec, section 7 is corrected for three things P2 changed:
  - sign-up with a known address;
  - verifying the email needs the password;
  - avatars upload to `pending/` first.

  It also gains a note on sign-up squatting: a pending sign-up is replaced by the newest, so a
  squatter cannot hold an address. Section 8 points here.
- `docs/memo/portal-organisations.md`, a decision record. It covers the counters as the race guard,
  the last-owner transaction, the inline resumable deletion, and the oracle fix.
- `docs/reference/portal.md` gains the new routes and error codes. The README's Portal section gains
  one line.

## 11. Non-goals

- Organisation-level SSO or domain-verified auto-join.
- Billing or per-organisation quotas set by the owner. Only an operator changes quotas (P4).
- Moving a client between workspaces.
- An organisation avatar.
- Operator screens for tenants and users (P4).
