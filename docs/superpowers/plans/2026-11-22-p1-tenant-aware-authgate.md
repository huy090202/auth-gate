# P1: tenant-aware AuthGate — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AuthGate knows which tenant owns a client, serves a scoped `/api/portal/*` API under its own key, enforces the client quota and tenant suspension itself, and can delete clients. stg gets cleaned and the smokes stop leaving clients behind.

**Architecture:** Ownership, quota and suspension live in AuthGate's table (`owner` on the client, `OWNER#<tenantId>` index and `META` items, written in the same transactions as the client). A tenant-scoped service view (`service.Tenant`) is the only path the portal routes use, so ownership is checked in one place. The portal routes are a second mux on the admin Lambda, behind `portal-key`.

**Tech Stack:** Go 1.26 (net/http ServeMux), DynamoDB (aws-sdk-go-v2), SAM/CloudFormation, bash, Bruno, React + Mantine (Console).

**Spec:** `docs/superpowers/specs/2026-11-22-self-service-design.md` — section 4 and the P1 row of section 11.

## Global Constraints

- `owner` is `""` for an operator's client and `T#<tenantId>` for a tenant's; `tenantId` passes `id.Validate` (ULID). It is set at creation and never changes through any route.
- Item keys: `OWNER#<tenantId>` / `C#<clientId>` (owner index: name, rpId, createdAt, disabledAt); `OWNER#<tenantId>` / `META` (`clientCount`, `clientQuota`, `suspendedAt`). Absent META or absent `clientQuota` means quota 5, count 0.
- Client attributes added: `owner` (S, absent for operator clients), `disabledBy` (S: `operator` or `tenant`), `deletedAt` (S, RFC3339 UTC). The `CLIENTS` index item also carries `owner` when set.
- Tenant-created clients get `rateLimit` burst 20, perSecond 10, whatever the body says.
- Error codes: `409 quota_exceeded`, `409 tenant_suspended`, `409 disabled_by_operator`, `400 invalid_tenant` (missing or non-ULID `x-tenant-id`), `400 invalid_client` for `rateLimit` in a portal PATCH; a client of another tenant, of an operator, or deleted answers `404 client_not_found`, exactly like an unknown id.
- Keys: admin routes keep `x-internal-access-key` = admin key; portal routes take `x-internal-access-key` = portal key and `x-tenant-id`. Neither key opens the other's routes (`401 unauthorized`).
- SSM: `/credo/auth-gate/<stage>/portal-key` (SecureString, `openssl rand -hex 32`). Env `PORTAL_KEY_PARAM` on the admin function; `PORTAL_KEY` in dev mode (default `dev-portal`).
- Owner META cache in the key lookup: 30 seconds per Lambda instance; operator clients never read it.
- Prune names: `smoke`, `bruno boundary A`, `bruno boundary B`, `bruno boundary C`, `bruno demo`, `bruno demo renamed`, `bruno rate limit`, `rate-limit-probe`. Never offered: the Console's own client (output `AuthGateClientId` of `auth-gate-console-<stage>`) and the Portal's (same output of `auth-gate-portal-<stage>`, absent in P1).
- Managed policies: `auth-gate-deployer-policy` (`infra/deployer-policy.json`) and `auth-gate-deployer-edge-policy` (`infra/deployer-policy-edge.json`), both attached to the user `auth-gate-deployer` and the role `ci-auth-gate-deploy`. No right is added or removed by the split.
- No step calls AWS. Never touch `~/.aws`. Secrets never on argv (send headers with `printf '…: %s\n' "$k" | curl -H @-`) and never printed.
- Store tests need DynamoDB Local: `make test-store` (or `DYNAMO_ENDPOINT=http://localhost:8000 go test ./...`). `make check` skips them, so every task touching the store runs `make test-store` too. Put `$(go env GOPATH)/bin` on `PATH` for golangci-lint.
- English in code, comments, docs and commits; Conventional Commits ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `pnpm run check` passes at the end of every task.

## Review Focus

- A rotate, update, disable or key write that lands after the client was deleted must fail, not resurrect it: every mutating store condition gains `attribute_not_exists(deletedAt)`. Test in Task 1.
- Deleting an already deleted client answers `404` and does not decrement `clientCount` twice. Test in Task 1.
- An operator lowers `clientQuota` below the current count: creates are refused, existing clients stay, deletes still decrement and the count never goes below 0. Test in Task 1.
- Legacy clients (no `owner`, `CLIENTS` items without `owner`) look the same on admin routes as today and are `404` on every portal route. Test in Task 3.
- A smoke run that fails part way still deletes the client it created. Test by reading in Task 5 (deferred delete); Bruno teardown runs even when earlier requests failed.

---

### Task 1: Ownership, quota and soft delete in the client store

**Files:**
- Modify: `internal/auth/client/client.go` (`Client`, `Summary`, `CreateInput`, errors, `Service.Create`, `Service.Get`, new methods)
- Modify: `internal/auth/client/store.go` (`Store` interface, `CreateClient`, `ListClients`, update conditions, new methods)
- Test: `internal/auth/client/client_test.go`

**Interfaces:**
- Produces in `client`:
  - fields `Client.Owner string`, `Client.DeletedAt *time.Time`, `Client.DisabledBy string`; `Summary.Owner string`; `CreateInput.Owner string`, `CreateInput.RateLimit *ratelimit.Settings`.
  - `type Owner struct { TenantID string; ClientCount, ClientQuota int; SuspendedAt *time.Time }`; `const DefaultClientQuota = 5`; `func OwnerOf(tenantID string) string` (returns `"T#"+tenantID`).
  - errors `ErrQuotaExceeded`, `ErrOwnerSuspended`.
  - `Service.Create(ctx, CreateInput)` honours `Owner` and `RateLimit`; `Service.Get` returns `ErrNotFound` for a deleted client; `Service.ListOwned(ctx, tenantID, cursor string) ([]Summary, string, error)`; `Service.Delete(ctx, id string) error`; `Service.GetOwner(ctx, tenantID) (Owner, error)` (defaults when absent); `Service.SetQuota(ctx, tenantID string, quota int) (Owner, error)` (0..1000, else `ErrInvalid`).
  - `Store` gains `ListOwned(ctx, tenantID, cursor string, limit int32) ([]Summary, string, error)`, `DeleteClient(ctx, c Client, at time.Time) error`, `GetOwner(ctx, tenantID) (Owner, error)`, `SetQuota(ctx, tenantID string, quota int) error`.

- [ ] **Step 1: Write the failing tests** in `client_test.go` (DynamoDB Local, `newSvc` pattern):
  - `TestOwnedCreateWritesFourItemsInOneTransaction`: create with `Owner: client.OwnerOf(t1)`; raw items exist for `C#<id>` (attr `owner = T#<t1>`, `rateLimitBurst 20` when `RateLimit` is `{20,10}`), `CLIENTS`/`C#<id>` (attr `owner`), `OWNER#<t1>`/`C#<id>`, `OWNER#<t1>`/`META` with `clientCount 1`. A seam store failing the transaction leaves none of them.
  - `TestOperatorCreateWritesNoOwnerItems`: no `owner` attr, no `OWNER#` items (legacy shape unchanged).
  - `TestQuotaHoldsUnderConcurrency`: 10 goroutines create for one tenant with default quota; exactly 5 succeed, 5 get `ErrQuotaExceeded`, `clientCount == 5`.
  - `TestLoweredQuotaRefusesCreatesKeepsClients`: 3 clients, `SetQuota(t, 1)`, create → `ErrQuotaExceeded`; deleting the 3 brings `clientCount` to 0, never negative; then 1 create succeeds, a 2nd fails.
  - `TestDeleteHidesClientAndFreesSlot`: after `Delete`, `Get`/`Update`/`Disable`/`Enable`/`RotateKey`/`RevokePreviousKeys` → `ErrNotFound`; absent from `List` and `ListOwned`; `Authenticate(rawKey)` → `ErrUnauthorized`; `clientCount` dropped by 1; a second `Delete` → `ErrNotFound` and `clientCount` unchanged.
  - `TestWriteAfterDeleteDoesNotResurrect`: with a seam store that deletes between `GetClient` and the write (the `afterGetStore` pattern), `UpdateClient`, `SetDisabled` and `SetKeys` fail with `ErrNotFound` and the item keeps `deletedAt`.
  - `TestListOwnedPagesOnlyThatTenant`: 3 clients for t1, 2 for t2, 1 operator; `ListOwned(t1)` with page size 2 returns t1's 3 over two pages; `List` returns all 6 with `Owner` set on the 5.
  - `TestGetOwnerDefaults`: unknown tenant → `{ClientCount 0, ClientQuota 5}`.

- [ ] **Step 2: Run them to see them fail**

Run: `make test-store` (or `DYNAMO_ENDPOINT=http://localhost:8000 go test ./internal/auth/client/...`)
Expected: compile errors for the new names.

- [ ] **Step 3: Implement.**
  - `CreateClient`: for an owned client the transaction adds the `OWNER#`/`C#` put and an `Update` on `OWNER#`/`META`: `SET clientCount = if_not_exists(clientCount, :zero) + :one` with condition `attribute_not_exists(suspendedAt) AND ((attribute_not_exists(clientQuota) AND (attribute_not_exists(clientCount) OR clientCount < :defaultQuota)) OR (attribute_exists(clientQuota) AND (attribute_not_exists(clientCount) OR clientCount < clientQuota)))`. On a cancelled transaction whose META item failed, read META and return `ErrOwnerSuspended` if `suspendedAt` is set, else `ErrQuotaExceeded`. The `CLIENTS` put carries `owner`.
  - `DeleteClient`: one transaction: client `Update SET deletedAt` with condition `attribute_exists(pk) AND attribute_not_exists(deletedAt)`; `Delete` of `CLIENTS`/`C#<id>`; for an owned client `Delete` of `OWNER#`/`C#<id>` and META `SET clientCount = clientCount - :one` with condition `clientCount > :zero`. Condition failure → `ErrNotFound`. `Service.Delete` then deletes the client's current and retired `K#` items best effort (log, do not fail: `Authenticate` already refuses).
  - Every existing mutating condition (`UpdateClient`, `SetDisabled`, `SetKeys`, `ClearRetiredKeys`, `indexUpdate` paths) gains `AND attribute_not_exists(deletedAt)`. `indexUpdate` also updates the `OWNER#` index item when the client is owned (name, disabledAt).
  - `GetClient` reads `owner`, `disabledBy`, `deletedAt`; `Service.Get` and `Authenticate` treat `DeletedAt != nil` as not found / unauthorized.
  - `ListOwned`: the `ListClients` query on `pk = OWNER#<tenantId>`, same cursor encoding and `limit+1` paging.

- [ ] **Step 4: Run the tests**

Run: `make test-store`
Expected: PASS, including every existing test in the package.

- [ ] **Step 5: Gate and commit**

Run: `pnpm run check` (green). Then:

```bash
git add internal/auth/client
git commit -m "feat(auth): clients have an owner, a per-tenant quota and a soft delete"
```

### Task 2: Suspension and who disabled a client

**Files:**
- Modify: `internal/auth/client/client.go` (`Service.Disable`, `Service.Enable`, `Authenticate`, owner cache)
- Modify: `internal/auth/client/store.go` (`SetDisabled`, suspend)
- Test: `internal/auth/client/client_test.go`

**Interfaces:**
- Consumes: Task 1's `Owner`, `GetOwner`, `OwnerOf`.
- Produces: `type DisabledBy string` with `ByOperator = "operator"`, `ByTenant = "tenant"`; `Service.Disable(ctx, id string, by DisabledBy) (Client, error)`; `Service.Enable(ctx, id string, by DisabledBy) (Client, error)` (tenant enabling an operator-disabled client → `ErrDisabledByOperator`); `Service.Suspend(ctx, tenantID string) (Owner, error)`, `Service.Unsuspend(ctx, tenantID string) (Owner, error)`; error `ErrDisabledByOperator`; `Store.SetDisabled(ctx, c Client, at *time.Time, by DisabledBy) error`; `Store.SetSuspended(ctx, tenantID string, at *time.Time) error`; field `Service.OwnerCacheTTL time.Duration` (default 30s when zero).

- [ ] **Step 1: Write the failing tests**:
  - `TestTenantCannotEnableOperatorDisabled`: `Disable(id, ByOperator)`; `Enable(id, ByTenant)` → `ErrDisabledByOperator`; `Enable(id, ByOperator)` succeeds and clears `disabledBy`. `Disable(id, ByTenant)` then `Enable(id, ByTenant)` succeeds.
  - `TestSuspendRefusesKeysWithinCacheTTL`: owned client authenticates; `Suspend(t)`; with `Now` advanced by 29 s `Authenticate` may still succeed from cache, at 31 s it is `ErrUnauthorized`; `Unsuspend` restores it after the next 31 s. An operator client issues no `OWNER#` read (seam store counting `GetOwner` calls stays 0).
  - `TestSuspendedOwnerCannotCreate`: `Suspend(t)`; `Create` for t → `ErrOwnerSuspended`.

- [ ] **Step 2: Run them to see them fail** — `make test-store`; compile errors.

- [ ] **Step 3: Implement.** `SetDisabled` writes or removes `disabledBy` with `disabledAt`. The owner cache is a mutex-guarded map `tenantID → (Owner, readAt)` on `Service`, read only when `c.Owner != ""`; a store error on the owner read fails closed (`ErrUnauthorized`). Existing callers of `Disable`/`Enable` pass `ByOperator`.

- [ ] **Step 4: Run the tests** — `make test-store`; PASS.

- [ ] **Step 5: Gate and commit** — `pnpm run check`; `git commit -m "feat(auth): tenants can be suspended, and an operator's disable outranks a tenant's"`.

### Task 3: Portal routes, operator routes and wiring

**Files:**
- Create: `internal/auth/service/tenant.go`, `internal/auth/handler/portal.go`, `internal/auth/handler/owners.go`
- Modify: `internal/auth/service/service.go` (operator wrappers), `internal/auth/handler/admin.go` (DELETE route, owners routes, views), `internal/auth/handler/respond.go` (error mapping), `internal/platform/config/config.go` (+ test), `cmd/auth-gate/main.go`, `cmd/smoke/main.go` (`url()` prefix rewrite), `internal/console/authgate/client.go` (`rolePath`), `bruno/opencollection.yml` (pre-request prefix)
- Test: `internal/auth/handler/handler_test.go`, `internal/platform/config/config_test.go`

**Interfaces:**
- Consumes: Tasks 1–2 (`client.Service` methods, errors).
- Produces:
  - `func (s *Service) Tenant(tenantID string) (*Tenant, error)` (`id.ErrInvalid` for a bad id). `Tenant` methods, each loading the client and answering `client.ErrNotFound` unless `c.Owner == client.OwnerOf(tenantID)`, and each write answering `client.ErrOwnerSuspended` while the owner is suspended: `Create(ctx, client.CreateInput) (client.Client, string, error)` (forces `Owner` and `RateLimit {20, 10}`), `List(ctx, cursor)`, `Get(ctx, id)`, `Update(ctx, id, client.Patch)` (`Patch.RateLimit != nil` → `client.ErrInvalid`), `Disable(ctx, id)`, `Enable(ctx, id)` (by tenant), `RotateKey(ctx, id)`, `RevokePreviousKeys(ctx, id)`, `PurgeSubject(ctx, id, subjectID)`, `Delete(ctx, id)`.
  - `Service` operator wrappers: `DeleteClient(ctx, id)`, `GetOwner(ctx, tenantID)`, `SetQuota(ctx, tenantID, quota)`, `SuspendOwner(ctx, tenantID)`, `UnsuspendOwner(ctx, tenantID)`.
  - `handler.NewPortal(svc *service.Service, portalKey string) http.Handler`, routes exactly as spec 4.2 under `/api/portal/clients…`; missing or invalid `x-tenant-id` → `400 invalid_tenant`.
  - Admin routes added: `DELETE /api/clients/{id}` → 204; `GET /api/owners/{tenantId}` → 200 `{tenantId, clientCount, clientQuota, suspendedAt?}`; `PATCH /api/owners/{tenantId}` body `{clientQuota}` → 200; `POST /api/owners/{tenantId}/suspend` and `/unsuspend` → 200. `clientView` and `summaryView` gain `owner` (the tenant id, omitempty) and `clientView` gains `disabledBy` (omitempty).
  - Config: `Config.PortalKeyParam` from `PORTAL_KEY_PARAM`, required for `RoleAdmin`. `main.go` mounts, for `RoleAdmin`, a mux sending `/api/portal/` to `NewPortal` and everything else to `NewAdmin`; dev mode mounts both under `/admin/` with `PORTAL_KEY` (default `dev-portal`).
  - The three local-prefix rewrites also map `/api/portal` and `/api/owners` to `/admin`.

- [ ] **Step 1: Write the failing tests** in `handler_test.go` (extend `env` with `portal http.Handler`; `const portalKey = "portal-secret"`; helper `(e) portal_(method, path, tenant, body string)`):
  - `TestPortalIsolation`: table over every portal route (get, patch, disable, enable, rotate, revoke-previous, purge-subject, delete) for three targets: a client of tenant B called as tenant A, an operator client, a deleted client of A. Every case answers `404` with body `{"error":"client_not_found"}`, and nothing changed (re-read through the admin route).
  - `TestPortalAndAdminKeysDoNotCross`: admin key on `GET /api/portal/clients` → 401; portal key on `GET /api/clients` and `DELETE /api/clients/{id}` → 401.
  - `TestPortalCreateForcesRateLimit`: create with body `rateLimit {burst 500, perSecond 500}` → 201; admin GET shows `burst 20, perSecond 10` and `owner` = tenant id. `PATCH` with `rateLimit` → `400 invalid_client`.
  - `TestPortalTenantHeader`: no header, `"nope"` → `400 invalid_tenant`.
  - `TestPortalQuotaAndSuspension`: 5 creates then the 6th → `409 quota_exceeded`; admin `PATCH /api/owners/{t}` `{clientQuota: 6}` → 6th succeeds; admin suspend → portal create and rotate → `409 tenant_suspended`, the tenant's key on verify → 401 after the cache TTL (set `OwnerCacheTTL: time.Nanosecond` in the test env so it is immediate; zero means the 30-second default).
  - `TestPortalEnableAfterOperatorDisable`: admin disable → portal enable → `409 disabled_by_operator`.
  - `TestAdminDeleteClient`: 204, then admin GET → 404, its key on verify → 401, it is gone from the list.
  - `TestLegacyClientViewUnchanged`: an operator client's admin view and summary have no `owner` and no `disabledBy` keys.
  - `config_test.go`: `RoleAdmin` without `PORTAL_KEY_PARAM` fails to load.

- [ ] **Step 2: Run them to see them fail** — `make test-store`; compile errors.

- [ ] **Step 3: Implement** per the Interfaces block. `respond.go` maps `client.ErrQuotaExceeded` → 409 `quota_exceeded`, `client.ErrOwnerSuspended` → 409 `tenant_suspended`, `client.ErrDisabledByOperator` → 409 `disabled_by_operator`; the tenant header error → 400 `invalid_tenant`. Admin `disable`/`enable` pass `client.ByOperator`.

- [ ] **Step 4: Run the tests** — `make test-store`; PASS. `go run ./cmd/auth-gate` in dev mode with `ADMIN_KEY=dev-admin` answers `GET /admin/api/portal/clients` with `400 invalid_tenant` given the portal key and no tenant.

- [ ] **Step 5: Gate and commit** — `pnpm run check`; `git commit -m "feat(auth): a portal key reaches only its tenant's clients; operators can delete clients and manage tenants"`.

### Task 4: Infrastructure, the portal key and the deployer policy split

**Files:**
- Modify: `infra/template.yaml` (admin function env `PORTAL_KEY_PARAM: /credo/auth-gate/${Stage}/portal-key`; its `ssm:GetParameter` resource list; CloudFront cache behaviours `/api/portal*` and `/api/owners*` → admin origin, same cache and origin request policies as `/api/clients*`)
- Modify: `scripts/setup-and-deploy/01-ssm.sh` (creates `admin-key`, `origin-verify`, `portal-key`, each only if absent, printing `kept <name>` for an existing one, the pattern of `scripts/console/deploy.sh`; values never on argv: build the `--cli-input-json` file as `deploy.sh` does), `scripts/setup-and-deploy/99-purge.sh` (also deletes `portal-key`), `scripts/setup-and-deploy/test/99-purge.sh` (expects it)
- Create: `infra/deployer-policy-edge.json` (the statements `CreateTheDistribution`, `ManageTheDistribution`, `DistributionPolicies`, `ManageTheEdgeFunctions`, `OptionalCustomDomain`, moved verbatim)
- Modify: `infra/deployer-policy.json` (those statements removed), `scripts/setup-and-deploy/00-deployer.sh` (creates or versions both managed policies and attaches both), `scripts/ci/00-ci-role.sh` (checks and attaches both)
- Modify: `package.json` only if a new script needs linting (none expected)

**Interfaces:**
- Consumes: Task 3's `PORTAL_KEY_PARAM`.
- Produces: the SSM parameter and both managed policies, used by the rollout in Task 7.

- [ ] **Step 1: Verify the split is a pure move**

Run: `jq -S '[.Statement[]] | sort_by(.Sid)' <(git show HEAD:infra/deployer-policy.json)` against `jq -S -s '[.[].Statement[]] | sort_by(.Sid)' infra/deployer-policy.json infra/deployer-policy-edge.json`, compared with `diff`.
Expected: no difference. Both files under 6144 non-whitespace characters (`tr -d ' \n\t' < f | wc -c`).

- [ ] **Step 2: Template and scripts** per the Files block; `sam validate --lint -t infra/template.yaml`.

- [ ] **Step 3: Tests and lint**

Run: `bash scripts/setup-and-deploy/test/99-purge.sh && pnpm exec shellcheck -x scripts/setup-and-deploy/*.sh scripts/ci/*.sh`
Expected: all ok; silent.

- [ ] **Step 4: Gate and commit** — `pnpm run check`; `git commit -m "feat(infra): portal-key and the portal and owner routes; the deployer policy splits in two"`.

### Task 5: Smokes that clean up, and the portal in Bruno

**Files:**
- Modify: `cmd/smoke/main.go` (after creating its client, `defer` a `DELETE /api/clients/{id}` that runs on every exit path, logging its result; the success path also asserts the delete answered 204 and that the client key then gets 401)
- Modify: `bruno/01-the-boundary/*setup-create-client*.yml`, `bruno/02-clients/01-create.yml`, `bruno/06-rate-limit/01-setup-create-client.yml` (each saves the new client's id in a runtime variable)
- Create: `bruno/07-portal/` (portal key as tenant A: create → 201 with forced rateLimit; as tenant B: get A's client → 404; admin key on a portal route → 401; portal key on `/api/clients` → 401; PATCH `rateLimit` → 400; delete → 204), `bruno/99-teardown/` (one request per saved id: `DELETE /api/clients/{{id}}`, skipped when the variable is unset, accepting 204 or 404)
- Modify: `bruno/opencollection.yml` (docs table: new routes; drop "cannot be deleted in v1"), `bruno/environments/*.example.yml` (`portalKey`), `scripts/setup-and-deploy/04-smoke.sh` (reads `portal-key` from SSM into the throwaway env file as `portalKey`)

**Interfaces:**
- Consumes: Task 3 routes; Task 4's SSM name.

- [ ] **Step 1: Local run.** `make dev` in one shell; in another, run the collection against `local` (`cd bruno && ../node_modules/.bin/bru run --env local --sandbox=developer`) and `go run ./cmd/smoke -base-url http://localhost:8080`.
Expected: every request passes; afterwards `GET /admin/api/clients` with the dev admin key lists no client named as in the prune list.

- [ ] **Step 2: Teardown on failure.** Make one request in `02-clients` fail on purpose (temporarily), run, confirm teardown still deleted the clients, then revert the change.

- [ ] **Step 3: Gate and commit** — `pnpm run check`; `git commit -m "test(smoke): smoke runs delete the clients they create; bruno covers the portal routes"`.

### Task 6: Operator cleanup scripts

**Files:**
- Create: `scripts/setup-and-deploy/06-prune-clients.sh`, `scripts/setup-and-deploy/test/06-prune-clients.sh`
- Create: `cmd/prune-factors/main.go`, `scripts/setup-and-deploy/07-prune-factors.sh`
- Modify: `package.json` (`test`: add `bash scripts/setup-and-deploy/test/06-prune-clients.sh`)

**Interfaces:**
- `06-prune-clients.sh [--keep <id>]... [--also <id>]...`: under `ADMIN_PROFILE` (default `default`), reads `admin-key` from SSM and the stack outputs `BaseUrl` (`auth-gate-<stage>`) and `AuthGateClientId` (`auth-gate-console-<stage>`, and `auth-gate-portal-<stage>` when that stack exists; "does not exist" is skipped, any other error stops). Lists every client (paged, field `clientId`), proposes those whose name is in the prune list, minus `--keep` ids and the protected ids, plus `--also` ids (an `--also` naming a protected id is refused). Prints id, name, rpId and createdAt for each, then asks `Type 'delete <N>' to delete these <N> clients:`; anything else deletes nothing. Deletes each with `DELETE /api/clients/{id}`, printing `deleted <id>` or the failure, and exits non-zero if any failed.
- `cmd/prune-factors -table <name> [-apply]`: scans `begins_with(pk, "S#")`, groups by client id, reads each client once, and for clients with `deletedAt` deletes their factor items; without `-apply` it only prints counts per client. `07-prune-factors.sh [--apply]` runs it under `ADMIN_PROFILE` like `05-backfill-clients.sh`.

- [ ] **Step 1: Write the failing test** `test/06-prune-clients.sh` with stub `aws` and stub `curl` first in `PATH` (the `99-purge.sh` harness): the stub API returns, over two pages, clients named `smoke`, `bruno demo`, `auth-app-local`, `auth-app-stg`, and a client named `smoke` whose id equals the Console's `AuthGateClientId`. Cases:
  - default run, confirmation `delete 2`: deletes exactly the two prune-named, non-protected ids; never the Console's id.
  - `--keep <smoke id>`: proposes 1.
  - `--also <auth-app-stg id>`: proposes 3. `--also <console id>`: exits non-zero before any delete.
  - wrong confirmation: no `DELETE` call.
  - every `curl` argv lacks the admin key (the stub records argv and fails the test if the key appears).
- [ ] **Step 2: Run to see it fail**, then implement the script; run again: all ok.
- [ ] **Step 3: `cmd/prune-factors`**: a store test (DynamoDB Local) with factors for a deleted client and a live client: dry run deletes nothing; `-apply` deletes only the deleted client's items. Put the logic in `internal/auth/factor` as `func PruneDeleted(ctx, db *dynamodb.Client, table string, apply bool) (map[string]int, error)` so the test calls it directly.
- [ ] **Step 4: Gate and commit** — `make test-store`, `pnpm run check`; `git commit -m "feat(ops): scripts to prune test clients and the factors of deleted clients"`.

### Task 7: The Console deletes clients and shows owners

**Files:**
- Modify: `internal/console/authgate/client.go` (`DeleteClient(ctx, id) error`)
- Modify: `internal/console/clients.go` (route `DELETE /api/clients/{id}`, audited `delete-client`, step-up, `errOwnClient` for the Console's own client, 204), `internal/console/stepup.go` (`delete-client` in `stepUpActions` with purpose `console-delete-client` and in `stepUpTargets` with `clientTarget`)
- Modify: `console-web/src/lib/clients.ts` (`owner?: string`, `disabledBy?: string` on `ClientSummary`/`ClientView`), `console-web/src/screens/Clients.tsx` (an Owner column: "Operator" or the tenant id's first 8 characters with the full id in a tooltip), `console-web/src/screens/ClientDetail.tsx` (a Delete section, hidden for the own client: a `TypeToConfirm` dialog asking for the client's name, then `stepUp('delete-client', …, id)`, then navigate to the list with a toast), `console-web/src/ui.tsx` (`TypeToConfirm` component: props `title`, `body`, `expected`, `confirmLabel`, `onConfirm`, `onCancel`; the confirm button enables only when the typed text equals `expected`), `console-web/src/stepup.tsx` (`StepUpAction` gains `delete-client`), `console-web/src/strings.ts` (EN and VI)
- Test: `internal/console/clients_test.go`, `console-web/test/strings.test.ts` (existing)

**Interfaces:**
- Consumes: Task 3's `DELETE /api/clients/{id}` and the `owner`/`disabledBy` view fields.

- [ ] **Step 1: Write the failing Go tests** (the existing `consoletest` fake AuthGate): `TestDeleteClientNeedsStepUp` (no token → `403 step_up_required`, no AuthGate call); `TestDeleteOwnClientRefused` (`409 own_client_protected`, no AuthGate call, token not spent); `TestDeleteClientAudited` (204, one AuthGate DELETE, an audit row `delete-client` with the client id as target).
- [ ] **Step 2: Run to see them fail**, implement the backend, run again: PASS.
- [ ] **Step 3: UI** per the Files block; `pnpm --filter console-web run typecheck && pnpm --filter console-web test`.
- [ ] **Step 4: Gate and commit** — `pnpm run check`; `git commit -m "feat(console): delete a client with typed confirmation and step-up; the list shows each client's owner"`.

### Task 8: Docs and rollout

**Files:**
- Create: `docs/memo/client-ownership.md` (decision record in the style of `console-sessions.md`: ownership in AuthGate not the Portal; a scoped key instead of the admin key; quota and suspension enforced by AuthGate; soft delete and why factors are pruned by a script; the 30-second suspension lag)
- Modify: `README.md` (routes table with the portal and owner routes; `portal-key`; "Deleting clients" with `06-prune-clients.sh` and `07-prune-factors.sh`; the two managed policies; a "Rollout of P1" block)

- [ ] **Step 1: Write the rollout** into README, paste-ready, in this order, all before the merge and from the `feat-self-service` worktree:
  1. `scripts/setup-and-deploy/01-ssm.sh` (adds `portal-key`, keeps the others).
  2. `ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh`, then `ADMIN_PROFILE=default scripts/ci/00-ci-role.sh` (the split; CI keeps every right).
  3. Merge: CI deploys and runs the smokes, which now delete their clients.
  4. `ADMIN_PROFILE=default scripts/setup-and-deploy/06-prune-clients.sh`, expecting 52 proposed on stg; then `07-prune-factors.sh`, then `07-prune-factors.sh --apply`.
  5. Read-only proofs: `aws iam list-attached-user-policies --user-name auth-gate-deployer` and `list-attached-role-policies --role-name ci-auth-gate-deploy` show both policies; the client list holds `auth-app-local`, `auth-app-stg`, `auth-gate-console-stg`.
- [ ] **Step 2: Gate and commit** — `pnpm run check`; `git commit -m "docs: client ownership, the portal key, pruning, and the P1 rollout"`.
