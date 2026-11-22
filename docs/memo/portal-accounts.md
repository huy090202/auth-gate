# The Portal's accounts: no enumeration, a mandatory second factor, and what that costs

## Decision

The Portal (`cmd/portal`, `internal/portal`, `portal-web/`, stack `auth-gate-portal-<stage>`) lets anyone make an account and manage their own AuthGate clients. Its accounts follow these rules:

- Every public route that takes an email (sign-up, resend, forgotten password, factor-reset request) answers `202 {"status":"check_email"}` whether or not the address has an account, and takes at least 1500 ms to do it.
- A sign-in failure is always `401 invalid_credentials`. An unverified email is revealed only after the right password (`403 email_unverified`).
- A second factor (a PIN or a passkey, checked by AuthGate) is mandatory: no session reaches `full` without one. Ten recovery codes are issued with the first factor.
- A recovery code signs the person in to stage `enroll`, not `full`, so the next screen is "add a new factor".
- A lost factor with no recovery code is reset by an emailed link, then a 24-hour wait that the next successful sign-in with the old factor cancels. The reset is applied lazily, at the first sign-in after the wait.
- Turnstile is always on for the four email routes, and on sign-in only after 2 failures in 15 minutes for the IP or for the account (keyed by the SHA-256 of the normalised email, so an unknown address looks like a known one).
- A password is checked against Have I Been Pwned's range API, and the check fails open.
- An avatar is uploaded straight to S3 with a presigned POST and confirmed by the server before it is set.

## Context

The Console has no public sign-up, so it can say "unknown user" in the same breath as "wrong password" and still leak nothing an operator did not already know. The Portal is open to the internet and its accounts hold the keys to other people's backends. A sign-up form that says "that email is taken" is a free lookup of who uses the service. The model is the Console's (server-side sessions, `FactorProved`, the first-factor lease, step-up tokens bound to action and target; see [console-sessions](console-sessions.md)), so the shared pieces live in `internal/platform` (`websession`, `lockout`, `lease`, `firstfactor`, `auditlog`, `password`, `webapi`) and the Portal keeps only its account lookup and routes.

## Sign-up without enumeration

A new address gets a verification link (24 hours). An address held by a pending (unverified) account is taken over by the new sign-up and gets a fresh link (below). A verified address gets a different email, "someone tried to sign up with this address", and the browser sees the same `202`. All three answers are the same bytes, and all take the same time:

- The route is padded to a floor of 1500 ms measured from the start of the request. A Lambda cannot keep working after it answers, so the send cannot be moved off the critical path, and without the floor the path that sends mail would be visibly slower than the path that does not.
- An internal failure on these routes (a token write, an SES error) is logged and still answers `202`. An error that appeared only for known addresses would be the same leak.
- The argon2id hash is paid on every sign-up, known address or not.
- The per-address limit (3 emails per hour) covers the "already registered" mail too, so the form cannot be used to mail-bomb a victim.
- Nothing a user typed is interpolated into any email, not even the display name: a `signup-exists` mail would otherwise carry a stranger's text to the address owner. Only the link and a date are filled in.

Emails are trimmed and lower-cased before any key or comparison, so `A@x.com ` and `a@x.com` are one account. A limiter key never holds an address, only its SHA-256. The email index item does hold the normalised address, because a unique index must key on it; that is visible to an operator with table read access, as in the Console.

An unverified account and its email index expire after 7 days (DynamoDB TTL), which frees the address. A verification link works once. A second use, an expired link and an unknown token all answer the same `400 invalid_token`. The link carries the token in the URL fragment, so it does not reach a log or a `Referer`.

### A pre-registered address does not win

Without more, a stranger could sign up first with someone else's address, wait for the owner to click the link the owner never asked for, and sign in with the stranger's own password to enrol the first factor. Two rules close that:

- The verification link asks for the account's password (`POST /api/auth/verify-email` takes `{token, password}`). The token is checked first (single use, purpose, expiry, the account's `sessionEpoch` and address at issue), then the password. A wrong password answers the same `400 invalid_token` as a bad link, counts like a failed sign-in (the lockout and the two Turnstile windows, keyed as sign-in keys them), and does not spend the link, so the owner can retype. A locked account is refused whatever the password. Only then is the address marked verified, in a write that also holds only at the epoch the password was checked at.
- A sign-up for an address held by a pending account replaces that account in one conditional write: the new password hash, display name and language, `sessionEpoch` up by one (every verify link already sent dies) and the 7-day TTL restarted. It answers the same `202` after the same floor and mails a fresh link. So the person who reads the mailbox and signs up last holds the password the link wants. The cost: two people racing for one pending address can keep displacing each other, within the per-address limit of 3 emails per hour; neither gets in without the mailbox.

## A mandatory second factor, and the recovery codes

The password alone never opens a workspace. After it, the session is `half` (10 minutes) until a PIN or passkey is verified at AuthGate with the purpose `portal-login`; an account with no factor goes to `enroll` (30 minutes) and cannot reach `full` until it adds one. A `full` session is 30 minutes idle and 8 hours absolute, and every request compares the session's `sessionEpoch` with the account's, so a password reset, a password change or an applied factor reset ends every other session. Five failures lock the account for 15 minutes, and the counter resets only on a completed sign-in (second factor or recovery code), not when the password is right: the lock exists to stop code guessing by someone who already has the password. The password, the second factor (start and verify) and recovery codes each have their own per-IP bucket (`login-ip`, `factor-ip`, `recovery-ip`, 10 then one per 6 seconds), and every request of a session at any stage spends the user's bucket (60, then one per second).

A sign-in attempt is written to the account's workspace audit log only once the password is right; before that it goes to the process log. Anyone can type an address, and a stranger's guesses must not fill the owner's log until the real entries scroll away. A wrong password on a live account records the lockout; an unknown, suspended or locked account makes one conditional write to a key that never exists, so both cost one write. What still differs: the failure that reaches the lock threshold makes a second write, a read and sends the owner the "account locked" mail, which only a live account can cause.

Recovery codes are 10 codes of 10 characters from Crockford's base32 (no I, L, O, U), shown as `XXXXX-XXXXX`, once. They are issued with the first factor, only after AuthGate accepted the write, so the stored count matches what the person saw. Only hashes are kept, `sha256(userId + "|" + normalised code)`: salting with the user id means one leaked table cannot be searched for a code across users. A code typed in lower case or without the dash still works, once. A wrong code counts as a login failure. Regenerating the set needs step-up and voids the old one.

A recovery code lands the person in `enroll` with `FactorProved` set, not in `full`. In `full`, adding a factor needs step-up with a factor, and the person has just said they have none: it would be a dead end. In `enroll` they add a new factor and move on; the cost is one extra screen.

## Lost factor: the lazy 24-hour reset

Without a recovery code, the person asks for a factor-reset link (1 hour). Confirming it does not purge anything: it records a pending reset due in 24 hours and mails the account owner ("a reset started; sign in to cancel"). A successful sign-in with an existing factor during the wait deletes the pending reset, and so does a factor added by a session that proved one (at sign-in, or with a recovery code), so the reset cannot later purge the factor just added. If nobody cancels it, the first sign-in after the wait, right after the correct password, purges the factors through the Portal's own AuthGate client, deletes the recovery codes (they no longer match a factor), and the person enrols again.

The reset is applied at sign-in rather than by a timer because the Portal has no scheduler and no fixed cost (a scheduled function would be a second Lambda, a rule and a queue for a rare event). The cost is that the factors still exist in AuthGate until that sign-in; nothing can use them without the password and the factor itself.

Reset and factor-reset links carry the account's `sessionEpoch` and the (normalised) address they were sent to, and are refused (`400 invalid_token`) once either has moved, so a password reset, a password change or an applied factor reset voids every link already sent, and so does an email change for the links sent to the old address. An email change also cancels a pending factor reset, which was confirmed from the old mailbox. One known gap: a cancelled reset does not bump the epoch (that would end the person's other sessions), so a stale confirm link can restart the wait, which is accepted because the owner is mailed and cancels by signing in.

## Turnstile, adaptive on sign-in

Turnstile is always required on sign-up, forgotten password, resend and factor-reset request. A Cloudflare failure on those answers `503 turnstile_unavailable`: we do not fail open on the routes that send mail. On sign-in it is required after 2 failures within 15 minutes for the client IP or for the account, answered `403 turnstile_required`, so a person who types the right password the first time never sees a challenge. Both windows are single items with a 15-minute TTL. The account window keys on `sha256(normalised email)` and counts unknown addresses too: keying on a user id would make "a challenge from a fresh IP" appear only for real accounts, which is an enumeration oracle. The price is that anyone can force a challenge on a victim's address, which is friction, not access. Locally and in tests Cloudflare's published test keys are used.

## HIBP fails open

A password has 12 to 128 bytes and is argon2id hashed with the Console's parameters. The range check sends only the first five hex characters of its SHA-1, waits 2 seconds, and on any failure accepts the password and logs one line. The alternative, refusing sign-ups while a third party is down, hands that third party a switch on our availability; the argon2id hash and the second factor are the real protection. Reset and factor-reset confirmation get their own per-IP bucket before the HIBP call, so a stream of tokens cannot make the Portal call HIBP without limit.

## Avatars: a presigned POST, then a server confirm

The browser asks for an upload form, posts the file to S3 directly (no credentials; conditions `content-length-range` up to 1 MiB and the content type, PNG, JPEG or WebP; key `pending/<userId>/<ulid>.<ext>`), then asks the API to confirm. The API `HeadObject`s the pending key, checks size and type (and that the type matches the key's extension), copies it to `avatars/<userId>/<ulid>.<ext>`, deletes the pending object, and only then sets it on the account. CloudFront and the bucket policy serve only `avatars/*`, and a lifecycle rule expires `pending/` after 1 day, so an upload that is never confirmed is never public and does not stay. Account deletion removes the confirmed avatar and relies on that expiry for any pending upload (the function has no right to list the bucket). The file never passes through the Lambda, so there is no body size limit to hit and no image decoding in a function that holds keys. CloudFront serves `/avatars/*` from the bucket through an origin access control, with `Content-Security-Policy: default-src 'none'; sandbox` so an uploaded object cannot run as a page.

## Alternatives rejected

- Saying "email already registered": a lookup for anyone.
- Answering fast on the path that does nothing: a timing oracle.
- Verifying from the link alone: the account's first password would be a stranger's.
- Mailing `signup-exists` to an address held by a pending account: the stranger who got there first keeps it.
- Sending the "already registered" mail without a limit: a mail bomb with our name on it.
- Letting a recovery code open `full`: it also opens step-up, which the lost factor was meant to guard.
- Purging the factors the moment the reset is confirmed: whoever controls the mailbox could remove the second factor in one click, with no time for the owner to react.
- A scheduled job to apply the reset: a fixed cost and a second moving part for a rare event.
- Turnstile on every sign-in: friction for everyone, to stop a few.
- Uploading avatars through the Lambda: a body limit, and image bytes in a function with the Portal's credentials.

## Consequences

- The four email routes always take about 1.5 s.
- An unconfirmed avatar upload lives under `pending/` for up to a day, unserved, then expires.
- A deleted avatar can stay in CloudFront's cache for up to 24 hours.
- Tenants register any `rpId` and origins without a domain-ownership check. The browser still binds a passkey to the origin it was made on, so this cannot be used to steal another site's passkeys, but a tenant can name a domain it does not own in its own client.
- The factors of an account with a due, unapplied reset exist in AuthGate until that account's next sign-in.
- Anyone can force a Turnstile challenge on a given address.
