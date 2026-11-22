# Client ownership, the portal key, and soft delete

## Decision

Six rules let a tenant manage its own clients without the admin key:

- AuthGate owns the tenant-to-client relation. A client carries `owner` (empty for an operator's client, `T#<tenantId>` for a tenant's), set at creation and never changed by any route.
- The Portal calls AuthGate with a second key, the `portal-key`, on `/api/portal/*`, plus an `x-tenant-id` header. The admin key does not open those routes and the portal key does not open `/api/clients*` or `/api/owners*`.
- AuthGate enforces the quota and the suspension, in the store's transactions and in key authentication, not the Portal.
- Tenant-created clients always get the rate limit burst 20, 10 per second, and a tenant cannot change it.
- A delete is soft: `deletedAt` is set, the client leaves every list and frees its quota slot. Factor items are pruned later by a script.
- A suspension reaches key authentication within 30 seconds, and the portal writes at once.

## Context

The Portal is a separate app with its own users, and its customers create clients. The only credential AuthGate had for creating clients was the admin key, which opens every client, every rate limit and the Console's own client. AuthGate keeps no list of who owns what, and tests of the two sides cannot see each other's data.

## Why ownership lives in AuthGate

If the Portal kept the tenant-to-client map, AuthGate would take the Portal's word for it on every call, and a Portal bug or leak would reach any client. With `owner` on the client, every portal route loads the client and compares its owner to `x-tenant-id` before anything else. A client of another tenant, of an operator, a deleted one and an unknown id all answer the same `404 client_not_found`, so the route does not tell a tenant which ids exist. The owner is written with the client and the `OWNER#<tenantId>` list item in one transaction, so a tenant's list reads that partition only and never scans.

The cost is that AuthGate learns the tenant concept. It stays small: an id, a count, a quota and a suspension mark, with no names or contacts.

## Why a scoped key and not the admin key

The admin key can read every client, set any rate limit, rotate the Console's client key and delete anything. Handing it to the Portal makes the Portal's worst bug the operator's worst day. The portal key reaches only the portal routes, where AuthGate itself narrows every call to one tenant. A Portal bug can at worst confuse two tenants. It cannot touch an operator's client or change a rate limit. A leaked portal key still lets its holder name any tenant id, so it is a secret of the same weight, but its reach is smaller and it rotates on its own (delete the SSM parameter, rerun `01-ssm.sh`, redeploy). Both keys sit in SSM, are read at cold start and are compared in constant time.

Both route sets run on the admin Lambda, behind the same CloudFront door and the same `x-origin-verify` check. A separate function would double the cold starts and the cost for no gain over a key check.

## Why quota and suspension are enforced in AuthGate

A quota checked in the Portal is a read followed by a write, and two requests can both pass it. Here the tenant's counters item (`OWNER#<tenantId>` / `META`: `clientCount`, `clientQuota`, `suspendedAt`) is part of the create transaction, with the conditions `clientCount < clientQuota` and no suspension, so the loser answers `409 quota_exceeded` or `409 tenant_suspended`. The key that the create wrote first is removed after a refusal. A delete decrements the count in its own transaction, once: deleting an already deleted client answers `404` and does not decrement again. An operator who lowers the quota below the count keeps the existing clients and blocks new ones; the count never goes below zero. An absent counters item or an absent `clientQuota` means quota 5 and count 0.

A tenant that can disable its own client cannot undo an operator's disable: the client records `disabledBy` (`operator` or `tenant`), and a tenant's enable of an operator's mark answers `409 disabled_by_operator`.

## Why the rate limit is forced

The rate limit is the operator's cost control. A tenant that could raise it could spend the operator's Lambda and DynamoDB budget. A tenant's create takes burst 20 and 10 per second whatever the body says, and a portal `PATCH` carrying `rateLimit` answers `400 invalid_client`. The operator raises it through the admin routes.

## Why a delete is soft, and factors are pruned by a script

A hard delete of a client has to remove its keys, its list items, its counters and every factor of every subject, and a crash part way leaves a half-deleted client that still authenticates. A soft delete is one conditional write of `deletedAt` and the count decrement. From then on the client stops authenticating, leaves every list and is `404` on every route. Every later write on it (rotate, update, disable, a key write) carries the condition that `deletedAt` is absent, so a call that lands after the delete fails instead of bringing the client back. The `K#` key items are removed best effort afterwards. A failure there is logged and harmless, because `Authenticate` refuses a deleted client.

The factor items (`S#<clientId>#<subject>`) are keyed by client and subject, with no index by client, so deleting them means a `Scan`. No Lambda role has `Scan` and none should get it. `07-prune-factors.sh` runs under the operator profile, prints counts without `--apply`, and deletes with it. A client with no `META` item, or without `deletedAt`, is left alone. Until it runs the factors stay in the table, reachable by no key.

## Why the suspension lags by up to 30 seconds

Authenticating a tenant's key would need one more read of the counters item on every verify call. Operator clients never read it. Each Lambda instance caches a tenant's counters for 30 seconds, so a suspended tenant's keys stop working within 30 seconds, instance by instance. A failed read of the counters denies the request. Suspending or unsuspending clears the cache of the instance that served the call only. Portal writes read the counters from the store, so they are refused at once. We accepted the lag because a suspension is an operator's slow decision, and the cache keeps every verify call at one read.

## Alternatives rejected

- The Portal keeps the tenant-to-client map: AuthGate would trust the Portal on every call.
- The admin key in the Portal: its worst bug reaches every client and every limit.
- Counting clients in the Portal: a read then a write races.
- A hard delete: a crash leaves a client with keys but no data; and it needs `Scan` anyway for the factors.
- Reading the counters on every authenticate: one more read on every verify call, to shorten a rare delay.
- A separate Lambda for the portal routes: a second cold start and a second set of roles, for the same key check.

## Consequences

- A rotate that loses a race to a delete can leave an orphan `K#` key item that points at a deleted client. `Authenticate` refuses it, so it is harmless clutter.
- A tenant's own `404` never says whether an id exists elsewhere.
- Test clients and factors of deleted clients accumulate until `06-prune-clients.sh` and `07-prune-factors.sh` run; both need the operator profile.
- A suspended tenant's keys keep working for up to 30 seconds on instances that have its counters cached.
- A leaked portal key lets its holder act as any tenant, to the extent of the portal routes. Rotate it as described above.
