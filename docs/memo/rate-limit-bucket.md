# The rate limit is a token bucket in the table

## Decision

The `verify` role limits each client with a token bucket. The bucket is one item, `RL#<clientId>` / `META`, in the single DynamoDB table, with `tokens` and `updatedAt` (integer microseconds). A new client holds 100 tokens and refills 50 per second. An empty bucket is a `429` with `{"error":"rate_limited"}` and a whole-second `Retry-After`. The `admin` role is not limited. Operators change one client's bucket with `PATCH /api/clients/{id}`; `perSecond` must be above 0.

## Context

The three Lambda roles scale out into many instances that share nothing, so a counter in memory limits one instance, not one client. The table is already a dependency of every request, so the shared state lives there and no new service is added.

## The condition

`updatedAt` marks the start of the refill window nobody has claimed yet. `tokens` holds up to 6 decimals and is calculated in integer micro-tokens, so the amounts in a condition are exact. Each call reads the item with a consistent read and computes `have = min(burst, tokens + floor(elapsed * perSecond))`. It then does one of these:

1. **No item.** `PutItem` with `tokens = burst - 1`, condition `attribute_not_exists(pk)`. A caller that loses this reads once more and continues below.
2. **Fewer than one token.** Denied, `Retry-After` from the shortfall. No write, so the window stays unclaimed and nothing is lost.
3. **Claim**, when `elapsed > 0` or `tokens > burst`. With `refill = have - tokens`, an `UpdateItem` sets `tokens = tokens + (refill - 1)` and `updatedAt = max(now, read + 1)`, condition `updatedAt = :read AND tokens >= 1 - refill`.
4. **Spend**, when the claim lost or there was nothing to claim. An `UpdateItem` sets `tokens = tokens - 1`, condition `tokens >= 1`. If this fails the caller is denied.

No caller retries, so an item that holds tokens serves callers at once instead of making them take turns.

Why this holds:

- **One claim per window.** Every claim strictly moves `updatedAt` and needs the value it read, so no elapsed time is credited twice.
- **Never above burst.** Only a claim adds tokens, and it adds to whatever remains after any spends since its read, so the result cannot exceed the value computed from that read.
- **Never below zero.** `tokens >= 1 - refill` still holds the claim back when spends land between the read and the claim. Without that clause `tokens` can go negative.
- **No refill from idle time after a spike.** The first writer on a full bucket claims the window although it earns nothing. A plain spend would leave `updatedAt` an hour old after an idle hour, and the next caller would be credited a full bucket again.
- **A lowered burst clamps.** After a `PATCH` lowers `burst`, `refill` is negative, so the next claim takes the stored tokens down to the new burst. A claim also fires when `tokens > burst`, and a spike overlapping a `PATCH` that lowers burst can briefly pass more than the new burst, bounded by the old stored tokens and ending at the first winning claim (reasoned from the design, not measured).

A clock behind `updatedAt` earns nothing and the call spends normally.

Cost: one read and at most two writes per allowed request, and a failed conditional write still consumes write capacity.

## Fail-open

If the table cannot be read or written, the call is allowed and the error is logged. The request needs the same table to do anything useful, so failing closed would add no protection and would turn a table outage into a second, louder one. A malformed bucket item is treated the same way.

That reasoning covers an outage of the table, not a flood on one bucket. A throttled read or write fails open like any other DynamoDB error, so a flood past DynamoDB's per-partition limits passes unmetered: the limiter stops limiting exactly when one client is flooding it. "The request needs the same table anyway" does not cover a hot `RL#` partition, because the rest of the request uses other partitions, which can still serve it. This is reasoned from AWS's documented limits, not measured. The behaviour follows spec section 5, and the product owner will decide on it separately.

## Measurement

`RATELIMIT_MEASURE=1 go test -run TestMeasureDefaults -v ./internal/platform/ratelimit` runs the limiter with the real clock and the default settings (burst 100, 50/s) against DynamoDB Local on a laptop under WSL2. Closed-loop workers call `Allow` on one key for 10 s each.

**DynamoDB Local latency is not AWS latency.** Local has no network hop and no replication. Read the shape, not the figures; this table says nothing about the numbers AWS would give.

| workers | allowed/s | denied |
| --- | --- | --- |
| 1 | 59.9 | 3184 |
| 5 | 59.9 | 9580 |
| 10 | 59.9 | 9512 |
| 20 | 59.9 | 8248 |
| 50 | 59.9 | 6398 |

Spike: 100 simultaneous calls on a full bucket, 5 times with 2.5 s between (long enough for a full refill).

| spike | allowed of 100 | denied |
| --- | --- | --- |
| 1 | 100 | 0 |
| 2 | 100 | 0 |
| 3 | 100 | 0 |
| 4 | 100 | 0 |
| 5 | 100 | 0 |

What the numbers show:

- The sustained rate holds at every worker count: allowed/s stays near 60, which is the refill (50/s) plus the initial burst spread over the 10 s window (100 / 10).
- A spike on a full bucket gets its whole burst.

This is one run on one machine with one key. It was not run against a deployed stack.

## Alternatives rejected

- A counter per Lambda instance: it limits an instance, not a client.
- A fixed window counter with `ADD`: no condition to fail, but it allows twice the limit across a window boundary and has no refill.
- Redis or another store: a new service to run for one table's worth of state.
- The optimistic version: read, then a whole-item `PutItem` with `updatedAt = :read`, 3 attempts in total with 5 to 25 ms of jitter. One writer wins per round, so on a full bucket of 100 a spike of 100 simultaneous calls allowed only 11 to 22 across two measurement runs, and the rest got `429` while tokens remained. The product owner replaced it on 2026-11-06.

## Known limit

One bucket is one item, and one item still has DynamoDB's per-partition limits. Every allowed call writes that item, and calls past those limits are not refused: they are throttled and pass unmetered (see Fail-open). So above that rate the configured `perSecond` no longer holds. This is reasoned from AWS's documented limits, not measured: DynamoDB Local does not reproduce them.

## Next step

Shard the bucket if a client outgrows one item: split its burst and rate over N items (`RL#<clientId>#<n>`), pick a shard per call, and let each shard spend only its own tokens. The cost is a coarser limit. Nothing has shown this is needed yet, so it is not built.
