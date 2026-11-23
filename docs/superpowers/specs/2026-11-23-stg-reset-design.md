# Reset stg's user data

Date: 2026-11-23. Status: approved by the owner in chat the same day.

## 1. Purpose

Weeks of testing left stg full of test accounts, tenant clients, smoke clients, factors and
tombstones. The owner wants a clean slate he can repopulate by hand, without touching the
infrastructure or the three apps' own clients.

Decided by the owner (not re-askable):

- Scope "Người dùng thử" ("test users"). It deletes:
  - every Portal account, workspace, session and audit item;
  - their tenant clients in AuthGate, and those clients' factors;
  - auth-app's demo users;
  - the old and junk clients.

  It keeps the Console admin accounts, the three current app clients, the stacks and the SSM
  parameters.
- Method: a script with a dry run. Nothing is deleted without `--apply` and a typed
  confirmation.

**Done when:**

- A dry run on stg prints, per table, what would be deleted and what is kept, with the three kept
  clients named.
- `--apply`, after `reset stg` is typed, leaves only the items listed as kept in §3.
- The AuthGate, Console, Portal and auth-app smokes pass afterwards.
- The owner signs in to the Console with his existing password and factor.

## 2. How

- A Go command, `cmd/stg-reset`, in the style of `cmd/prune-factors`. It runs under an operator
  profile, because it needs `Scan`, which no Lambda role has.
- A wrapper, `scripts/setup-and-deploy/08-reset-data.sh [--apply]`, does three things:
  - it reads the keep set from the stacks;
  - it runs the command (a dry run without `--apply`);
  - with `--apply`, it asks for the typed confirmation first.
- **The keep set** is the three app clients:
  - the Console's: output `AuthGateClientId` of `auth-gate-console-stg`;
  - the Portal's: the same output of `auth-gate-portal-stg`;
  - auth-app's: the parameter `AuthGateClientId` of `auth-app-stg`, which has no output.

  If any of the three is missing, empty, unknown to AuthGate, disabled or soft-deleted, the
  script stops before it deletes anything. A wrong keep set would delete an app's own client.
- **Guards:**
  - The stage must be `stg`; any other value is refused.
  - The dry run prints the keep set with each client's name and rpId.
  - `--apply` needs the exact answer `reset stg`.
  - It is safe to rerun: a second run finds nothing to delete.
- **Deletes** use `BatchWriteItem` in groups of 25 and retry unprocessed items. Each table is
  scanned with a projection of `pk`, `sk` and, for `K#` items, `clientId`.

## 3. What goes and what stays

The full map of item types, with file:line references, came from reading the code. This section
lists the rules.

**AuthGate, `auth-gate-stg`:**
- Kept, for each client K in the keep set:
  - `C#<K>/META` and `CLIENTS/C#<K>`;
  - every `K#…` item whose `clientId` is K;
  - `RL#<K>`;
  - `CH#<K>#…` and `R#<K>#…`.
- Kept: `S#<console client>#…`, the Console admins' PINs and passkeys. Without them every admin
  enrols again.
- Deleted: everything else. That covers:
  - every other client, live or soft-deleted, with its list items;
  - every `OWNER#…` item (tenant indexes and counters);
  - every other `K#…`;
  - all factors under the Portal's and auth-app's clients (their users are deleted) and under
    every other client;
  - every other `RL#`, `CH#` and `R#`.

**Console, `auth-gate-console-stg`:**
- Kept: `ADMINS/A#…`, the admin accounts.
- Deleted: `SES#…` (sessions), `JTI#…` (used tokens), `RL#…` (rate buckets).
- Deleted: `AUDIT#…`, the operator's audit trail. Decided by the owner: "xoá audit luôn"
  (delete the audit too). It records actions on clients that the reset deletes.

**Portal, `auth-gate-portal-stg`:**
- Deleted: every item. The table holds only user data, its indexes, and ephemeral items:
  users, emails, workspaces, memberships, workspace audit, recovery codes, factor resets, email
  tokens, sessions, used tokens, failure counters and rate buckets. No configuration lives in it.
- The avatar bucket (stack output `AvatarBucket`): every object under `avatars/` and `pending/`.

**auth-app, `auth-app-stg`:**
- Deleted: every item (`U#…` users and `I#…` indexes). Its sessions are cookies, so signed-in
  demo users are simply signed out by the next request.

## 4. Tests

- **The rule set as a pure function**, `Decide(table, item, keep) (delete bool, reason string)`.
  One test row per item type above, for kept and deleted cases:
  - a `K#` item of a kept client against one of another client;
  - `S#<console>` against `S#<portal>`;
  - a tombstone of a kept client's id. This cannot happen: the keep check refuses it.
- **Against DynamoDB Local** (`make test-store`): seed the four tables with one item of each type
  and a fake keep set, run with apply, and assert that exactly the kept items remain. A second run
  deletes nothing.
- **The wrapper, with a stub `aws`:**
  - a stage other than stg is refused;
  - a missing keep id stops it before the command runs;
  - a wrong answer to the prompt deletes nothing;
  - without `--apply` the command runs in dry-run mode.

## 5. Rollout

1. A dry run on stg; the owner reads the counts and the keep set.
2. `--apply`, and type `reset stg`.
3. Run the four smokes.
4. Sign in to the Console. Sign up on the Portal and in the auth-app demo again.

## 6. Non-goals

- Resetting other stages or production.
- Deleting stacks, buckets other than the avatars, SSM parameters or the Console admins.
- A reset button in any app.
- Hard delete as a product feature: this is an operator tool for stg.
