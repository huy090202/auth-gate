# The operator's side: HTTP with its own key, AuthGate first, an internal reason

## Decision

The Console manages the Portal's customers by these rules:

- The Console calls the Portal over HTTP, on `/api/operator/*`, with a key of its own. It never reads the Portal's table, and the Portal never holds the admin key.
- When an action touches both services, the Console calls AuthGate first and the Portal second. If the second call fails, the audit entry says `partial`, and repeating the action finishes it.
- A suspension needs a reason. The reason is internal: it stays in the Console's audit and is never sent to the Portal or to the customer.
- AuthGate refuses a suspended tenant's keys within 30 seconds, the age of its owner cache. Nothing forces that sooner.
- An operator can never read a password, a recovery code or a client key, never sign in as a user, and never hard-delete a tenant or a user.

## Context

Self-service customers live in the Portal, and until now the Console could not see them. An operator needed to look up a tenant, change its quota, stop an abuser, or help a person who lost their factors. Any Console admin may do this, each action needs step-up, and each is audited. The design is `2026-11-26-operator-design.md`; this note gives the reasons.

## Why HTTP and its own key, not table access

The Console's Lambda role could be given read access to the Portal's table. It would be simpler, and it would couple the two apps to one schema, and let a Console bug or a stolen Console role read every user and every session. The Portal and the Console share code only through `internal/platform`; sharing a table would break that rule from the other side.

So the Portal exposes a small API and decides what leaves it. No route returns a hash, a recovery code, a token or a session, so the Console cannot ask for them. The key is its own SSM SecureString (`/credo/auth-gate-portal/<stage>/operator-key`). The admin key and the portal key do not open it, and it opens neither of theirs ([client-ownership](client-ownership.md)). Only the Portal and the Console read it. The Portal compares it in constant time and limits it with a bucket per key, so a leaked key is rate limited as well as revocable. The cost is one more secret, one script (`operator-key.sh`) and a call between two Lambdas.

## Why AuthGate first, and `partial`

Suspending a tenant has two parts. AuthGate stops the keys, which is what protects the service. The Portal marks the workspace read-only and mails the owners, which is what the customer sees. The two cannot be one transaction: they are two services.

AuthGate goes first because it is the part that matters. If the Portal call fails, the abuser's keys are already dead, and the worst state is a workspace that still looks writable. The other order could leave the keys working while the Portal says "suspended", which is the wrong lie. Both calls are idempotent, so the operator repeats the action. The audit entry records `result: partial`, the tenant page shows an "Out of sync" warning with a button for the missing step, and the Portal sends one email however many times the call is repeated: only a call that changed the state mails.

A quota change follows the same order: AuthGate sets it, and the Portal only writes the tenant's audit.

## Why the reason stays internal

The reason is for the other admins ("abuse report 4411", "chargeback"). Sent to the customer it could leak an internal case, a third party's report or a name. The customer's email has fixed text and the support address, and the customer writes to support to ask. The Portal therefore never receives the reason, so it cannot log it, mail it or show it. It is 1 to 200 characters, trimmed, and checked before the step-up token is spent, so a refused reason costs no approval. Unsuspending takes none: lifting a suspension has nothing to justify.

The tenant's own audit shows the action as "AuthGate administrator" with the fixed actor id `operator`, never the operator's name, so customers learn what was done and not who did it.

## Why 30 seconds, and what a suspension does not do

AuthGate caches an owner's state for 30 seconds so that a client key check costs no extra read. A suspended tenant's keys therefore keep working for up to 30 seconds. The alternative is a read on every verification, which costs a request and a DynamoDB read for each call all the time, to speed up a rare action. Thirty seconds is shorter than any human response to an abuse report, so the cache stays.

A suspension does not delete anything. The tenant, its clients, its audit and its members stay for the operator to inspect, and a member may still leave. A suspended workspace answers `409 tenant_suspended` to every write, and deleting an account that would delete one is refused for the same reason. A suspended user is signed out everywhere (the epoch bump, as in [console-sessions](console-sessions.md)) and cannot sign in, and the answer is the same `401` a wrong password gives.

## What an operator can never do

By construction, not by policy:

- Read a password or a recovery code. Both are hashed, and no operator route returns one.
- Read a client key. It is shown once at creation; an operator can only rotate it.
- Sign in as a user. There is no impersonation route, and a factor reset only starts the 24-hour wait and the emails to the person.
- Hard-delete a tenant or a user, or act in bulk.
- Browse every user. The search is one exact email.
- Touch the Portal's own AuthGate client. Rotate, disable, delete and purge answer `409 own_client_protected`, as they do for the Console's: the Portal's users would be locked out.
- Run an action without step-up, which is bound to the admin, the action and the target.

## Other trade-offs

- Tenants and users are paged and filtered in the browser by name over the pages already loaded. A search over every tenant would need an index the Portal does not have, and the operator's list is short.
- Each Console action costs two calls. The Console's Lambda keeps its timeout, and a slow Portal gives `502 portal_unavailable`, with the cause in the log.
- Mail to owners and users is best effort, like every other notice: the state change is done, and a retry that changes nothing would not send it.
