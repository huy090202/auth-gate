# Portal organisations: counters, owners, deletion and the open invite lookup

## Decision

Organisations (P3) follow these rules:

- Every limit is a counter on the tenant item (`memberCount`, `ownerCount`, `inviteCount`) or on the user item (`orgsCreated`), checked as a condition in the transaction of the write it guards. Nothing counts by querying first.
- A change that can remove an owner runs as one transaction: the membership pair, the condition `ownerCount > 1` on the tenant, and a ConditionCheck that the actor still has the role the handler saw. A person acting on themselves drops that ConditionCheck.
- `SetRole` and `RemoveMember` are bound to the role the handler checked. The store takes the target `Membership`, and its condition `role = :old` makes the write fail if that role moved in between.
- Deleting an organisation runs inline in the request that asks for it, and resumes from the next `DELETE` or the next workspace list. There is no scheduler, queue or timer.
- Signing in with the right password on an unverified account answers exactly like a wrong password.
- The invite lookup works signed out, and the token is its only gate.

## Context

The Portal is one Lambda on one table. DynamoDB gives conditional writes and transactions of up to 100 items, and nothing else that fences concurrent requests. Anyone can create an organisation, up to 3 each, invite 20 people a day, and the people invited have no account yet when the link is made. The parent spec (`2026-11-22-self-service-design.md`, section 8) and the P3 design (`2026-11-24-p3-organisations-design.md`) give the rules; this note gives the reasons.

## Why counters, not counting queries

"At most 20 members" is a count, and count-then-write races: 21 requests all read 19, all pass, and the organisation ends with 40. A query before the write cannot be made safe, because the write that follows is a separate call.

So each limit is a number on an item the write already touches, and the write is conditional on it: `memberCount < 20` on accept, `inviteCount < 20` on invite, `orgsCreated < 3` on create. DynamoDB serialises writes to one item, so two requests cannot both pass. The counter and the thing it counts change in the same transaction, so they cannot drift apart while the transaction is the only way to change either.

The cost is that every path that adds or removes a counted item has to move the counter too. Two paths needed care:

- Invites carry no DynamoDB `ttl`. A TTL would delete an expired invite without decrementing `inviteCount`, and the organisation would lose a slot for good. The only thing that removes an expired invite is the prune that runs when invites are listed or created: it deletes the expired items it finds and decrements the counter in the same transaction. An expired invite therefore holds a slot until someone lists or creates, and one of those happens before any new invite is checked against 20.
- `orgsCreated` goes back down when an organisation is deleted. That is done once, by a flag on the tenant item (`creatorReleased`) set in the same transaction, so a deletion that runs twice does not release twice. A creator whose account is gone only gets the flag set.

## Why the last-owner transaction, and why the self case drops a check

"An organisation always has an owner" is a count too. Two owners demoting each other both read `ownerCount` 2, both pass, and none is left. It is the Console's master guard (`console-sessions.md`) applied to a tenant: the transaction holds the membership pair (`TENANT#/MEMBER#` and `USER#/TENANT#`), the tenant's counters with `ownerCount > 1` when an owner goes, and a ConditionCheck on the actor's own membership item (`role = actor.Role`).

Two owners acting on each other touch each other's membership items, and DynamoDB cancels a transaction when it conflicts with one in flight, so one is refused and the other lands. The ConditionCheck also stops an actor whose role changed since the guard read it.

A transaction may not name an item twice. When the actor is the target (leaving, or stepping down), the ConditionCheck and the membership write would be the same item, so the ConditionCheck is dropped. The membership write's own condition, `role = :old` with the role the handler checked, does the same job: if the actor's role moved, it fails. `SetRole` and `RemoveMember` also refuse a self-call whose target role differs from the actor's role before writing.

That `role = :old` condition is also why the store takes the target `Membership` and not a user id. The handler decides "an admin cannot touch an owner" from the target's role. If the store looked the role up again, the target could be promoted in between and the admin would demote an owner. Binding the write to the checked role turns that window into a `409 conflict` the person can retry.

## Why deletion is inline and resumable

Deleting an organisation deletes its clients at AuthGate, then its invites, memberships and store items. That is many calls and can stop half way: AuthGate can be down, or the Lambda can time out. The options were a scheduler or a queue, which cost infrastructure and fixed money that a portfolio project with no users should not carry (decision of 2026-11-20: fixed-cost features are off by default), or a design where every step can run again.

The second one holds. `deletingAt` is set first, so the organisation is unusable at once: the member guard answers `404 not_found` on every route except its own `DELETE`, and the workspace list leaves it out. A deleting personal workspace is different and keeps P2's `409 tenant_deleting`. Then `finishOrgDeletion` runs, and each step skips what is already gone:

1. list and delete each client at AuthGate (404 is skipped);
2. delete the invites and their index items;
3. drop every other member's membership pair and mail them `org-deleted`;
4. the store finish: release `orgsCreated`, delete the `TENANTS` index item, set 30-day TTLs on the tenant item and its audit;
5. drop the acting owner's membership last.

A failure answers `502` and keeps `deletingAt`. The next `DELETE` by an owner runs it again, and so does the next `GET /api/tenants` by any member still holding a membership item, which finds the tenant with `deletingAt` and calls the same function. Nobody has to remember.

The actor's membership is dropped after the store finish, not before. Dropped first, a failure in the finish could not be resumed by the actor, who would no longer be a member and so fail the guard. Held until the end, it is the handle a resume uses. Every other member's item serves the same purpose through the workspace list. The audit entry `org-delete` is written at once, before the 30-day TTL is set; written when the request ends, it would carry its own 180 days and outlive the organisation.

Account deletion uses the same function for each organisation the person is alone in. It sets `deletingAt` only while the organisation has exactly one member (`MarkDeletingIfAlone`). If someone accepted an invite in the meantime, the answer is `409 sole_owner` with that organisation's name, and nothing is lost.

A known residual: a client create that passed the guard just before `deletingAt` was set can reach AuthGate after the run listed the clients. That client is left behind at AuthGate, in a tenant the Portal can no longer reach. Closing it would need a fence AuthGate does not have. The window is the length of one request, and the orphan gets no access from the Portal.

## Why the oracle fix

Until P3, an attacker could sign up with a victim's address X and a password P of their own, then sign in with X and P. `403 email_unverified` meant no verified account held X (the pending sign-up took it). `401` meant a verified account did. That is an enumeration oracle, the thing every email route was built not to be.

Now sign-in with the right password on an unverified account answers the same `401 invalid_credentials`, counts as a failure for the lockout and the Turnstile threshold, and takes the same path, so status, body and side effects match a wrong password. The sign-in screen always offers "Didn't get the verification email? Resend", and the resend route already answers the same whether or not the address exists.

The cost, accepted: five right-password sign-ins on an unverified account now lock it for 15 minutes, and the lock also blocks its verify link for that time. A person who signs up and then tries to sign in five times before verifying has to wait. That is a rare, harmless path, and it is what closes the oracle: no refusal is allowed to differ by whether the account is verified.

## Why the invite lookup works signed out

The invite page (`/invite/<tenantId>#token=<token>`) must show a signed-out visitor who invited them, to which organisation and as what role, and prefill sign-up with the invited address. The person has no account to sign in with yet, so the lookup `POST /api/invites/{tenantId}/lookup` takes no session. The token is the only gate:

- It is 32 random bytes, only its SHA-256 is stored, and it travels in the link's fragment and in POST bodies, so it never reaches CloudFront or a log.
- Whoever holds it received the email, and so already knows the invited address and the organisation. The lookup reveals nothing they do not have.
- An unknown, expired or used token, a tenant that is gone, a tenant being deleted and a personal workspace all answer the same `404 invite_not_found`.
- It has a per-IP limit shared with sign-in, so tokens cannot be guessed at speed.
- With a full session it adds `emailMatches`, so the page can say the invite is for another address and offer no button. Nothing on the page says whether the invited address has an account, which keeps sign-up enumeration-free.

Accept and decline do need a full session, and the session's address must equal the invited one (`403 invite_wrong_email`). A leaked link alone makes no member.

The web keeps a pending invite in `localStorage` under `portal.invite`, for at most 24 hours. Verifying an email opens a new tab, and a person who signs up from an invite must come back to it in that tab: a `sessionStorage` copy would be gone. It is cleared on accept, decline, a 404, a wrong address and Back, so a stale token does not follow the person around. If storage is blocked the link in the email still works.

## Other trade-offs

- Roles and removals need step-up; leaving does not. A person can always walk away, and nothing is lost that the owner cannot reverse.
- Invite items hold an opaque `inviteId`, not the token hash, so the invite list never holds anything that opens a link.
- Mail on removal, role change and deletion is best effort: a failed mail does not undo the change.
