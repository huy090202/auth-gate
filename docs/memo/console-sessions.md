# Console sessions, guards and step-up tokens

## Decision

Five rules in the Console keep a session honest:

- A session carries the account's `sessionEpoch`, and every request reads the account and rejects a mismatch.
- A change that can remove a master runs as one transaction that also checks the actor is still an active master. The operator CLI has no actor, so it uses a surviving master as one.
- The Console's own AuthGate client is read-only through the Console.
- Step-up is asked in stage `full`, never in `enroll`. A session that has not proved a factor (`FactorProved`) cannot write or become `full` once the account has a factor, and the first factor is written under a short lease on the account.
- A step-up token is bound to the admin, the action and the target.

## Context

Sessions are server-side items keyed by the hash of the cookie, with no index, so "end this admin's sessions" cannot list them. Admin accounts are managed by other admins, and a temporary password is handed from one person to another. Factors live in AuthGate, which the Console calls over HTTP and which does not fence writes.

## Why the epoch

Resetting, clearing or disabling an account must end its sessions at once. The session key is a hash and there is no GSI, so the sessions cannot be found. Instead the account holds a counter, each session copies it, and the check on every request compares them. Bumping the counter ends every session at one write, and old session items die by TTL. The cost is one consistent read of the account per request.

## Why the actor is a condition in the master guard

"The last active master cannot be disabled or demoted" is a count, and count-then-write races: two masters acting on each other can both pass the count and leave none. The write is a transaction that also checks the actor is still an active master. An actor cannot target itself, so it survives its own change, and two masters acting on each other touch each other's items, so one transaction is cancelled and refused. The operator CLI has no actor. It picks another active master as the survivor, then runs the same transaction with that master as the condition, so a master removed in between makes the write fail. The cost is that a CLI run can answer a conflict and need rerunning.

## Why the Console's own client is read-only

The Console is itself a client of AuthGate, and every admin's factors are subjects of that client. Through the Console, a role `admin` could purge a master's factors, which breaks "only `master` manages accounts". Rotating its key from the UI would leave the SSM copy stale, so sign-in would die when the 24 hours ended. Patching its origins could lock everyone out. So rotate, revoke previous keys, patch, disable and purge on it answer `409 own_client_protected`. The scripts and the CLI remain for the emergency.

## Why `FactorProved` and the first-factor lease

A temporary password is a bearer secret held by two people. The attack: someone with an old or stolen temporary password signs in to an account that already has a factor, lands in `enroll`, and, because enrolling a first factor needs no step-up, replaces the owner's PIN and reaches `full`. The rule "first factor needs no step-up" means a session that actually has none, so an enroll session now carries `FactorProved`: set when it passed a second factor, or when it enrolled the first factor itself with none existing. Without it, and with a factor present, the write is refused and the session ends.

That check reads the factors and then writes, and the two are separate calls to AuthGate, so two sessions could both read zero and both write. A narrower fix, claiming the account by bumping its epoch before the write, left a gap: a sign-in during the write read the new epoch and zero factors and claimed too. So the Console takes a lease on the account for the first-factor write: the loser answers `409 enrollment_in_progress`, makes no AuthGate call and stays signed in. The lease lasts 25 seconds, counted from the claim, and covers a read and a write. A write that got no answer may still land, so its lease is kept until it runs out; an answered failure releases it.

The guarantee is bounded by timeouts, not absolute: AuthGate does not fence writes. A sent write lands within AuthGate's 10 second function timeout, so the Console sends the first-factor write only while the time since the claim plus those 10 seconds stays under the lease: the write carries a deadline at the claim plus 13 seconds (25 less 10 less a 2 second margin), and one that could only go out later is not sent, answers `502 authgate_unavailable` and releases the lease. One cut off after it may have gone out keeps the lease, as does an answered 5xx. The Console Lambda's 24 second timeout does not give this bound: it limits when a write is sent, not when it lands, and the DynamoDB calls before the write retry without a deadline of their own. Whoever holds a temporary password and acts before its owner can still enroll first; the owner then finds they cannot sign in, and the master who issued it can reset again.

## Why step-up tokens are bound to targets

A token bound only to the admin and the action works on any target: approved for `rotate-key` on client A, it rotates client B within its 300 seconds. The challenge purpose is therefore the action's purpose, a colon, and the first 32 hex characters of `sha256(target)`. The target is the client id; `<clientId>/<subjectId>` for a purge; the normalized username for admin actions; `<username>/<role>` for creating an admin or changing a role, so an approval to create an `admin` cannot create a `master`. The token is also single use. The binding is to the target, not the body: an approved update of a client can change any field of that client, once. Binding the body too would need the SPA to hash what it sends; the session cookie and the token are both needed already.

## Alternatives rejected

- Listing sessions by admin through a GSI: a fixed index cost for a rare event, where a counter costs a read.
- Count the masters, then write: races, as above.
- Accepting the first-factor window: the attack above lands silently.

## Consequences

- Every authenticated request costs one consistent read of the account.
- Other tabs sign in again after a password change, which bumps the epoch.
- A first-factor retry after a crash or a write with no answer can wait up to 25 seconds.
- The limit stays written down: a lease with timeouts, not a fence.
