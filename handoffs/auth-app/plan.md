# auth-app Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship auth-app v1: one site that is both the integration docs for auth-gate (EN/VI) and a working sample client (username/password + PIN + passkey, all three auth-gate scenarios) with a trace panel showing every call the sample backend makes to auth-gate.

**Architecture:** One SPA (`app/`: React + Vite + Mantine, routes `/docs/*` and `/app/*`) served from S3, and one Hono backend (`bff/`) on a Lambda Function URL, both behind one CloudFront distribution so the browser only ever calls same-origin `/api`. `bff` owns users (one DynamoDB table) and sessions (HS256 cookie with an `mfa` flag), relays PIN/WebAuthn to the deployed auth-gate stg, and verifies returned tokens against auth-gate's JWKS. Trace entries are recorded per request and returned in the response body.

**Tech Stack:** Node 22, pnpm 10.13.1 workspace, TypeScript strict NodeNext, Hono 4 (`@hono/node-server`, `hono/aws-lambda`), `@aws-sdk/lib-dynamodb`, `@aws-sdk/client-ssm`, `jose`, `ulid`, `node:crypto` scrypt, Vite 6, React 19, Mantine 7, react-router 7, `marked`, Vitest, `node --test` for bff, AWS SAM, DynamoDB Local (docker compose), Bruno OpenCollection.

**Spec:** `docs/superpowers/specs/2026-10-20-auth-app-design.md` (landed by Task 0 from auth-gate `orchestrator:handoffs/auth-app/spec.md`).

## Global Constraints

- Code, comments, docs, commit messages in English. Chat in Vietnamese.
- Error envelope on every non-2xx: `{"error": "<snake_case_code>", ...extras}`; when `TRACE=1`, every response that called auth-gate (2xx or not) also carries `trace: [...]`.
- Header to auth-gate `x-internal-access-key`; CloudFront origin header `x-origin-verify`; SSM paths `/auth-app/<stage>/session-secret`, `/auth-app/<stage>/origin-verify`, `/auth-app/<stage>/authgate-client-key`. Region `ap-southeast-1`. Stage default `stg`, AWS profile default `auth-app-deployer`.
- Session cookie `auth_app_session`, HttpOnly, Secure, SameSite=Lax, Path=/; half session (`mfa=false`) 10 min, full session 12 h.
- Username: 3–32 chars, `[a-z0-9_.-]`, lower-cased; `#` therefore impossible. Password: 8–128 chars. PIN is passed through to auth-gate unchanged (its policy decides).
- Step-up purposes allow-list: `["change-email"]`.
- Trace redaction: `pin` → `"***"`, `credential` → `{"redacted": true}`, `token` and `stepUpToken` → first 16 chars + `…`; request headers are never recorded.
- The auth-gate base URL is always the deployed stage (`AUTHGATE_BASE_URL`); nothing in this repo runs auth-gate.
- Tests only where spec §7 puts them plus the Review Focus tests below; no AWS SDK mocks, no coverage targets. `bff` tests run with `node --import tsx --test`; `app` tests with Vitest.
- Commit after every task with a conventional message. Steps marked **HUMAN** (AWS deploys, SSM writes, registering clients on auth-gate, pushes) are run by the human.
- Demo screens are EN only; docs pages exist in both `docs/en` and `docs/vi` with identical file names and frontmatter.

## Review Focus

1. `POST /auth/login` with an unknown username must answer exactly like a wrong password: `401 invalid_credentials`, no `next`, and the scrypt compare still runs against a fixed dummy hash so timing does not reveal existence (test in Task 5).
2. `POST /auth/second-factor/start` with no cookie must be `401 unauthorized`; with a full session (`mfa=true`) it must be `409 already_verified`, never a second challenge (test in Task 3 for the middleware, Task 5 for the route).
3. `POST /stepup/start` with a purpose outside the allow-list (`"delete-account"`) must be `400 invalid_purpose` before any auth-gate call, so a caller cannot mint tokens for purposes the app never checks (test in Task 5).
4. `POST /me/email` with a `stepUpToken` issued for another user, another purpose, or already expired must be `403 step_up_required` and the email unchanged (test in Task 5 using tokens minted with the Task 4 test key).
5. A deep link such as `/docs/step-up` must return `index.html` with 200 from CloudFront, while `/api/does-not-exist` must return bff's `404 not_found` JSON, not the SPA (checked by `04-smoke.sh` in Task 10).

---

## File structure

```
package.json  pnpm-workspace.yaml  .npmrc  .gitignore  docker-compose.yml  .github/workflows/ci.yml
tools/wt/  .claude/  docs/infra/         from the auth-gate bootstrap (not touched here)
docs/superpowers/{specs,plans}/          spec and this plan
docs/en/*.md  docs/vi/*.md               the eight docs pages, both languages
bff/package.json  bff/tsconfig.json  bff/.env.example
bff/src/config.ts                        loadConfig(): env + SSM at cold start
bff/src/lib/http.ts                      jsonOk, jsonError, AppError, errorHandler, notFound
bff/src/lib/trace.ts                     TraceRecorder, redact
bff/src/lib/dynamo.ts                    documentClient(endpoint?), ensureTable (local only)
bff/src/lib/origin-verify.ts             originVerify(secret) middleware
bff/src/users/store.ts                   UserStore interface, DynamoUserStore, MemoryUserStore
bff/src/users/password.ts                hashPassword, verifyPassword (scrypt), DUMMY_HASH
bff/src/users/validate.ts                parseUsername, parsePassword, parseEmail
bff/src/session/cookie.ts                issueSession, readSession
bff/src/session/middleware.ts            requireSession({ mfa })
bff/src/authgate/client.ts               AuthGate class: one method per relayed route, error mapping, trace
bff/src/authgate/token.ts                verifyAuthGateToken (the file readers copy)
bff/src/routes/auth.ts  me.ts  stepup.ts
bff/src/app.ts                           buildApp(deps): Hono
bff/src/server.ts  bff/src/lambda.ts
bff/test/*.test.ts
app/package.json  app/vite.config.ts  app/index.html  app/tsconfig*.json
app/src/main.tsx  App.tsx  routes.tsx  theme.ts
app/src/lib/api.ts                       api<T>(path, init): same-origin fetch, pushes trace
app/src/lib/webauthn.ts                  toCreationOptions, toRequestOptions, serializeCredential
app/src/lib/i18n.ts                      useLang(): 'en' | 'vi' in localStorage
app/src/trace/TraceStore.tsx  TraceDrawer.tsx
app/src/docs/loader.ts  DocsLayout.tsx  DocsPage.tsx
app/src/app/{Register,Login,SecondFactor,PasskeyLogin,Home,Security}.tsx  StepUpModal.tsx
app/src/app/session.tsx                  useMe(), SessionGuard
app/test/webauthn.test.ts
infra/template.yaml  infra/deployer-policy.json
scripts/setup-and-deploy/lib.sh 00-deployer.sh 01-ssm.sh 02-register-client.sh 02-build.sh 03-deploy.sh 04-smoke.sh 99-purge.sh
bruno/opencollection.yml  bruno/environments/local.example.yml  bruno/01-auth/ 02-me/ 03-stepup/
README.md  CONTRIBUTING.md
```

---

### Task 0: Land the docs and the pnpm workspace skeleton

Precondition (HUMAN, before the worktree exists): the `auth-app` repository is bootstrapped per auth-gate `docs/infra/06-bootstrap-new-project.md` variant A, so `tools/wt`, `.claude`, `docs/infra`, `CLAUDE.md`, `package.json`, `.github/workflows/ci.yml` are on `main` and this task runs in worktree `feat/auth-app`.

**Files:**
- Create: `docs/superpowers/specs/2026-10-20-auth-app-design.md`, `docs/superpowers/plans/2026-10-20-auth-app.md`, `pnpm-workspace.yaml` (`packages: [app, bff]`), `docker-compose.yml`, `bff/package.json`, `bff/tsconfig.json`, `bff/src/server.ts` (placeholder), `app/package.json`, `app/index.html`, `app/vite.config.ts`, `app/src/main.tsx` (placeholder "auth-app")
- Modify: `package.json` (root scripts), `.gitignore`, `.github/workflows/ci.yml`, `CLAUDE.md` first paragraph

**Interfaces:**
- Produces: root scripts `lint` = shellcheck on `scripts/setup-and-deploy/*.sh tools/wt/*.sh tools/wt/test/*.sh .claude/**/*.sh` plus `pnpm -r lint`; `test` = round-1 shell tests plus `pnpm -r test`; `typecheck` = `pnpm -r typecheck`; `build` = `pnpm -r build`; `check` = `lint && typecheck && test && build`. `bff` scripts: `dev` (`tsx watch --env-file-if-exists=.env src/server.ts`), `build` (`esbuild src/lambda.ts --bundle --platform=node --target=node22 --format=esm --outfile=dist/index.mjs` with a banner that defines `require` for CJS deps), `typecheck`, `test`, `lint` (eslint). `app` scripts: `dev`, `build` (`tsc -b && vite build`), `typecheck`, `test` (`vitest run`), `lint`.
- `docker-compose.yml`: service `dynamodb` (`amazon/dynamodb-local`, `-jar DynamoDBLocal.jar -inMemory -sharedDb`, port 8000).

- [ ] **Step 1: Copy spec and plan** — `git show <auth-gate orchestrator>:handoffs/auth-app/spec.md` is not reachable from this repo; the human provides both files (they are in the auth-gate orchestrator worktree at `handoffs/auth-app/`). Commit them under `docs/superpowers/`.
- [ ] **Step 2: Write the workspace files above; `.gitignore` adds `dist/`, `.aws-sam/`, `bff/.env`, `bruno/environments/*.yml` except `*.example.yml`.**
- [ ] **Step 3: CI: `actions/setup-node@v4` (node 22), `pnpm/action-setup@v4`, `pnpm install --frozen-lockfile`, a `dynamodb` service container on 8000, then `pnpm run check`.**
- [ ] **Step 4: Run `pnpm install && pnpm run check`** — Expected: green with placeholder packages.
- [ ] **Step 5: Commit** — `chore: land auth-app spec and plan; pnpm workspace with app and bff`

---

### Task 1: bff foundation — http envelope, trace, config, origin check

**Files:**
- Create: `bff/src/lib/http.ts`, `bff/src/lib/trace.ts`, `bff/src/config.ts`, `bff/src/lib/origin-verify.ts`, `bff/test/trace.test.ts`, `bff/.env.example`

**Interfaces:**
- Produces:
  ```ts
  class AppError extends Error { constructor(public status: number, public code: string, public extras: Record<string, unknown> = {}) }
  type Vars = { trace: TraceRecorder }                   // Hono Variables
  function jsonOk(c: Context<{Variables: Vars}>, status: 200 | 201, body: object): Response   // adds trace when enabled
  function jsonError(c, err: AppError): Response                                              // {error, ...extras, trace?}
  const errorHandler: ErrorHandler   // AppError → jsonError; anything else → 500 internal
  const notFound: NotFoundHandler    // 404 {error: "not_found"}
  type TraceEntry = { method: string; path: string; requestBody: unknown; status: number; responseBody: unknown; ms: number }
  class TraceRecorder { constructor(enabled: boolean); record(e: TraceEntry): void; entries(): TraceEntry[] | undefined }  // undefined when disabled
  function redact(body: unknown): unknown   // deep copy; keys pin, credential, token, stepUpToken per Global Constraints
  type Config = { stage: string; table: string; authGateBaseUrl: string; authGateClientId: string; authGateClientKey: string; sessionSecret: string; originVerify: string | null; trace: boolean; dynamoEndpoint?: string }
  async function loadConfig(env = process.env): Promise<Config>   // SSM only when AUTHGATE_CLIENT_KEY/SESSION_SECRET/ORIGIN_VERIFY are unset and STAGE is set; local uses .env
  function originVerify(secret: string | null): MiddlewareHandler   // null → pass-through (local); else header mismatch → 401 unauthorized (constant-time compare)
  ```
- `.env.example`: `PORT=8787 TABLE=auth-app-local DYNAMO_ENDPOINT=http://localhost:8000 AUTHGATE_BASE_URL= AUTHGATE_CLIENT_ID= AUTHGATE_CLIENT_KEY= SESSION_SECRET=dev-secret TRACE=1`.

- [ ] **Step 1: Write failing tests in `bff/test/trace.test.ts`**

```ts
test('redact hides pin, credential, token and stepUpToken at any depth', ...)
  // {pin:'123456', nested:{credential:{id:'x'}}, token:'a'.repeat(40), stepUpToken:'b'.repeat(40)}
  // → pin '***', nested.credential {redacted:true}, token 'aaaaaaaaaaaaaaaa…', stepUpToken 'bbbbbbbbbbbbbbbb…'
test('redact leaves other fields and arrays intact', ...)          // {subjectId:'u1', factors:['pin']} unchanged, input not mutated
test('TraceRecorder returns undefined when disabled and entries in order when enabled', ...)
test('TraceRecorder stores redacted bodies, never the originals', ...)   // record({requestBody:{pin:'1'}}) → entries()[0].requestBody.pin === '***'
```

- [ ] **Step 2: Run `pnpm --filter bff test`** — Expected: FAIL (module not found).
- [ ] **Step 3: Implement the four files.** `loadConfig` reads SSM with `GetParameter` `WithDecryption: true` for the three names under `/auth-app/<stage>/`.
- [ ] **Step 4: Run `pnpm --filter bff test`** — Expected: PASS.
- [ ] **Step 5: Commit** — `feat(bff): error envelope, redacted trace recorder, config and origin check`

---

### Task 2: bff users — DynamoDB store, scrypt passwords, input validation

**Files:**
- Create: `bff/src/lib/dynamo.ts`, `bff/src/users/store.ts`, `bff/src/users/password.ts`, `bff/src/users/validate.ts`, `bff/test/validate.test.ts`

**Interfaces:**
- Produces:
  ```ts
  type User = { userId: string; username: string; email: string; passwordHash: string; createdAt: string }
  interface UserStore { create(u: User): Promise<void>   /* throws AppError(409,'username_taken') */; getByUsername(username: string): Promise<User | null>; getById(userId: string): Promise<User | null>; updateEmail(userId: string, email: string): Promise<void> }
  class DynamoUserStore implements UserStore { constructor(doc: DynamoDBDocumentClient, table: string) }   // pk `U#<username>`, sk `META`; getById via a second item `I#<userId>`/`META` {username} written in the same TransactWrite
  class MemoryUserStore implements UserStore {}                                                            // tests and Bruno-less local runs
  function documentClient(endpoint?: string): DynamoDBDocumentClient
  async function ensureTable(doc, table): Promise<void>   // local only: create pk/sk table if missing
  async function hashPassword(p: string): Promise<string>          // "scrypt$<saltB64>$<hashB64>", N=2^15 r=8 p=1, 32-byte salt, 64-byte key
  async function verifyPassword(p: string, stored: string): Promise<boolean>   // timingSafeEqual
  const DUMMY_HASH: string   // a real hash of a random string, used when the user does not exist
  function parseUsername(v: unknown): string   // throws AppError(400,'invalid_username'); lower-cases
  function parsePassword(v: unknown): string   // AppError(400,'invalid_password')
  function parseEmail(v: unknown): string      // AppError(400,'invalid_email'); contains one '@', ≤ 254 chars, lower-cased
  ```

- [ ] **Step 1: Write failing tests in `bff/test/validate.test.ts`** (Review Focus support: usernames are the only id we own)

```ts
test('parseUsername accepts 3–32 of [a-z0-9_.-] and lower-cases', ...)   // 'Huy.09' → 'huy.09'
test('parseUsername rejects #, spaces, 2 chars, 33 chars, non-string', ...)   // each → AppError 400 invalid_username
test('parsePassword rejects < 8 and > 128', ...)
```

- [ ] **Step 2: Run `pnpm --filter bff test`** — Expected: FAIL.
- [ ] **Step 3: Implement the four source files.**
- [ ] **Step 4: Run tests** — PASS. Then `docker compose up -d && DYNAMO_ENDPOINT=http://localhost:8000 TABLE=auth-app-local node --import tsx -e "..."` creating and reading one user through `DynamoUserStore` — Expected: round trip works, second `create` throws `username_taken`.
- [ ] **Step 5: Commit** — `feat(bff): user store on dynamodb, scrypt passwords, input validation`

---

### Task 3: bff session — cookie and `mfa` gate

**Files:**
- Create: `bff/src/session/cookie.ts`, `bff/src/session/middleware.ts`, `bff/test/session.test.ts`

**Interfaces:**
- Produces:
  ```ts
  type Session = { userId: string; mfa: boolean }
  const COOKIE = 'auth_app_session'
  async function issueSession(c: Context, secret: string, s: Session): Promise<void>   // HS256 via jose SignJWT {sub, mfa, exp}; exp +10 min when !mfa, +12 h when mfa; sets cookie per Global Constraints
  async function readSession(c: Context, secret: string): Promise<Session | null>     // null on missing/invalid/expired
  function clearSession(c: Context): void
  function requireSession(secret: string, opts: { mfa: boolean }): MiddlewareHandler
    // no/invalid cookie → 401 unauthorized
    // opts.mfa === true  and session.mfa === false → 401 second_factor_required
    // opts.mfa === false and session.mfa === true  → 409 already_verified   (Review Focus 2)
    // sets c.var.session
  ```

- [ ] **Step 1: Write failing tests** (a tiny Hono app with `GET /full` behind `{mfa:true}` and `GET /half` behind `{mfa:false}`)

```ts
test('no cookie → 401 unauthorized on both routes', ...)
test('half session reaches /half and gets 401 second_factor_required on /full', ...)
test('full session reaches /full and gets 409 already_verified on /half', ...)
test('cookie signed with another secret → 401 unauthorized', ...)
test('issueSession sets HttpOnly; Secure; SameSite=Lax; Path=/ and Max-Age 600 for half, 43200 for full', ...)
```

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement.** **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(bff): signed session cookie with an mfa flag and a route gate`

---

### Task 4: bff authgate — client wrapper, error mapping, token verification

**Files:**
- Create: `bff/src/authgate/client.ts`, `bff/src/authgate/token.ts`, `bff/test/token.test.ts`

**Interfaces:**
- Produces:
  ```ts
  type Factor = 'pin' | 'webauthn'
  class AuthGate {
    constructor(opts: { baseUrl: string; clientKey: string; fetch?: typeof fetch })
    setPin(trace: TraceRecorder, subjectId: string, pin: string): Promise<void>
    deletePin(trace, subjectId): Promise<void>
    factors(trace, subjectId): Promise<{ pin: boolean; webauthn: { credentialId: string; createdAt: string; lastUsedAt?: string }[] }>
    startRegistration(trace, subjectId, displayName?): Promise<{ registrationId: string; publicKey: unknown }>
    finishRegistration(trace, registrationId, credential: unknown): Promise<{ credentialId: string }>
    deleteCredential(trace, subjectId, credentialId): Promise<void>
    createChallenge(trace, subjectId, purpose: string, factor: Factor): Promise<{ challengeId: string; expiresAt: string; publicKey?: unknown }>
    verifyChallenge(trace, challengeId, body: { pin: string } | { credential: unknown }): Promise<{ token: string; expiresAt: string; factor: Factor }>
  }
  // every method: one fetch to `${baseUrl}/api/...`, header x-internal-access-key, records {method, path, requestBody, status, responseBody, ms} on trace, then maps per spec §4.7:
  //   400 verification_failed → AppError(400,'verification_failed',{attemptsRemaining}); 423 locked → AppError(423,'locked',{lockedUntil}); 410 → AppError(410,'challenge_gone');
  //   404 factor_not_found → AppError(409,'factor_not_enrolled'); 400 registration_failed/pin_rejected → same code 400; fetch error or ≥500 → AppError(502,'authgate_unavailable'); other 4xx → AppError(502,'authgate_unavailable',{upstream: code})
  type AuthGateClaims = { iss: string; sub: string; aud: string; purpose: string; amr: string[]; jti: string; exp: number }
  async function verifyAuthGateToken(token: string, opts: { jwks: JWTVerifyGetKey; issuer: string; audience: string; subject: string; purpose: string }): Promise<AuthGateClaims>
    // jose jwtVerify with algorithms ['ES256'], issuer, audience; then sub and purpose equality; any failure → AppError(403,'step_up_required')
  function remoteJwks(baseUrl: string): JWTVerifyGetKey   // createRemoteJWKSet(new URL('/.well-known/jwks.json', baseUrl), {cooldownDuration: 300_000, cacheMaxAge: 300_000})
  ```

- [ ] **Step 1: Write failing tests in `bff/test/token.test.ts`** — generate a P-256 key with `jose.generateKeyPair('ES256')`, build `jwks = createLocalJWKSet({keys:[publicJwk with kid 'k1']})`, mint tokens with `SignJWT` (`{purpose, amr:['pin'], jti}`, `iss 'https://ag.test'`, `aud 'cl_1'`, `sub 'u1'`, exp +300):

```ts
test('valid token returns its claims', ...)
test('token signed by another key → 403 step_up_required', ...)
test('wrong aud → 403', ...)
test('wrong purpose → 403', ...)      // minted for 'change-email', verified for 'transfer'
test('wrong sub → 403', ...)
test('expired token → 403', ...)
```

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement both files.** **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(bff): auth-gate client with error mapping and token verification`

---

### Task 5: bff routes, app wiring, local server, Lambda entry, local client registration

**Files:**
- Create: `bff/src/routes/auth.ts`, `bff/src/routes/me.ts`, `bff/src/routes/stepup.ts`, `bff/src/app.ts`, `bff/src/server.ts` (replace placeholder), `bff/src/lambda.ts`, `bff/test/routes.test.ts`, `scripts/setup-and-deploy/lib.sh`, `scripts/setup-and-deploy/02-register-client.sh`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces:
  ```ts
  type Deps = { config: Config; users: UserStore; authGate: AuthGate; jwks: JWTVerifyGetKey }
  function buildApp(deps: Deps): Hono<{ Variables: Vars & { session?: Session } }>
  ```
  Routes exactly as spec §4.3, mounted under `/api`; `originVerify` first, then a middleware that creates `TraceRecorder(config.trace)` per request; `errorHandler`, `notFound`. Login: `getByUsername`, `verifyPassword(password, user?.passwordHash ?? DUMMY_HASH)`, 401 `invalid_credentials` if either fails (Review Focus 1); then `factors()` on auth-gate: none → full session + `{next:'done'}`, else half session + `{next:'second-factor', factors:{pin, webauthn: boolean}}`. `second-factor/*` behind `requireSession({mfa:false})`; `/me/*` and `/stepup/*` behind `{mfa:true}`. `stepup/start` checks the allow-list before calling auth-gate (Review Focus 3). `me/email` calls `verifyAuthGateToken` with `subject = session.userId`, `purpose 'change-email'`, `issuer config.authGateBaseUrl`, `audience config.authGateClientId` (Review Focus 4). `passkey/start` for an unknown username or a user without passkeys → `409 factor_not_enrolled` (same body).
  `server.ts`: `loadConfig()`, `documentClient(DYNAMO_ENDPOINT)`, `ensureTable` when an endpoint is set, `@hono/node-server` on `PORT`. `lambda.ts`: `handle(app)` from `hono/aws-lambda`, deps built once at module load.
- `lib.sh`: same shape as auth-gate's (`STAGE`, `AWS_PROFILE` default `auth-app-deployer`, `REGION`, `STACK=auth-app-$STAGE`, `PARAM_PREFIX=/auth-app/$STAGE`, `die`). `02-register-client.sh stg|local`: requires `AUTHGATE_BASE_URL` and `AUTHGATE_ADMIN_KEY` env; `local` → `POST $AUTHGATE_BASE_URL/api/clients {name:"auth-app-local", rpId:"localhost", origins:["http://localhost:5173"]}` and writes `AUTHGATE_CLIENT_ID`/`AUTHGATE_CLIENT_KEY`/`AUTHGATE_BASE_URL` into `bff/.env` (refuses to overwrite an existing key); `stg` → reads `BaseUrl` from the stack outputs, `rpId` = its host, `origins` = `[BaseUrl]`, writes the key to SSM `authgate-client-key` (SecureString) and prints the client id for `03-deploy.sh` (`AUTHGATE_CLIENT_ID` env).

- [ ] **Step 1: Write failing tests in `bff/test/routes.test.ts`** using `buildApp` with `MemoryUserStore`, a fake `AuthGate` (same class shape, scripted responses), and the Task 4 local JWKS:

```ts
test('register then login without factors → next done and a full cookie', ...)
test('login unknown username → 401 invalid_credentials with no next', ...)             // Review Focus 1
test('login with a pin factor → next second-factor and a half cookie; /api/me → 401 second_factor_required', ...)
test('second-factor/start with a full session → 409 already_verified', ...)             // Review Focus 2
test('second-factor/verify success upgrades the cookie; /api/me then 200', ...)
test('stepup/start purpose delete-account → 400 invalid_purpose and the fake auth-gate was not called', ...)   // Review Focus 3
test('me/email with a token for another sub, another purpose, or expired → 403 step_up_required; email unchanged', ...)   // Review Focus 4
test('responses that called auth-gate carry trace with redacted pin', ...)
test('GET /api/nope → 404 not_found JSON', ...)
```

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement routes, `app.ts`, `server.ts`, `lambda.ts`, the two scripts.** **Step 4: Run tests** — PASS; `pnpm run check` green.
- [ ] **Step 5: HUMAN — `AUTHGATE_BASE_URL=https://d2x14lcxloom8y.cloudfront.net AUTHGATE_ADMIN_KEY=… scripts/setup-and-deploy/02-register-client.sh local`, then `docker compose up -d && pnpm --filter bff dev`. Check with curl: register, login, `POST /api/me/pin {pin:"123456"}` → 201 with `trace[0].path === "/api/factors/pin"`; logout, login → `next: "second-factor"`.**
- [ ] **Step 6: Commit** — `feat(bff): auth, me and step-up routes; local server; lambda entry; client registration script`

---

### Task 6: app foundation — shell, theme, api client, WebAuthn helpers, trace drawer

**Files:**
- Create: `app/src/main.tsx`, `App.tsx`, `routes.tsx`, `theme.ts`, `lib/api.ts`, `lib/webauthn.ts`, `lib/i18n.ts`, `trace/TraceStore.tsx`, `trace/TraceDrawer.tsx`, `app/test/webauthn.test.ts`, `app/test/fixtures/{creation-options,request-options,attestation,assertion}.json`
- Modify: `app/vite.config.ts` (dev proxy `/api` → `http://localhost:8787`, no path rewrite)

**Interfaces:**
- Produces:
  ```ts
  class ApiError extends Error { status: number; code: string; extras: Record<string, unknown> }
  async function api<T>(path: string, init?: { method?: string; body?: unknown }): Promise<T>   // same-origin fetch, JSON; on any body with `trace` → traceStore.push(entries); non-2xx → throw ApiError
  function toCreationOptions(publicKey: unknown): PublicKeyCredentialCreationOptions   // base64url → ArrayBuffer on challenge, user.id, excludeCredentials[].id
  function toRequestOptions(publicKey: unknown): PublicKeyCredentialRequestOptions     // challenge, allowCredentials[].id
  function serializeCredential(cred: PublicKeyCredential): object   // {id, rawId, type, response:{clientDataJSON, attestationObject | authenticatorData+signature+userHandle}, clientExtensionResults} all base64url, the shape go-webauthn parses
  const traceStore: { entries: TraceEntry[]; push(e: TraceEntry[]): void; clear(): void; subscribe(fn): () => void }  // useSyncExternalStore
  function TraceDrawer(): JSX.Element   // floating button bottom-right with a count badge; drawer lists entries `POST /api/challenges → 201 (84 ms)`; each expands to request/response JSON; Clear; Copy as curl (`-H 'x-internal-access-key: <client key>'`)
  function useLang(): ['en' | 'vi', (l: 'en' | 'vi') => void]   // localStorage key `auth-app.lang`, default 'en'
  ```
  `routes.tsx`: `/` → redirect `/docs/what-it-is`; `/docs/:page` (Task 8); `/app/*` (Task 7). `?trace=1` on any URL opens the drawer on load. Theme: Mantine light/dark following `prefers-color-scheme` with a toggle; a monospace font for code and trace.
- Fixtures: `creation-options.json` and `request-options.json` are real option bodies from auth-gate (run `make dev` in the auth-gate repo and copy, or take them from a stg trace); `attestation.json`/`assertion.json` are the serialized credentials `cmd/smoke` posts (log them once from a local run of `cmd/smoke` with `-v`, or from the `04-webauthn` Bruno requests).

- [ ] **Step 1: Run `/impeccable` in this worktree with the brief: "docs site + demo for a backend auth service; readers are developers; human voice, no hero, no gradients, no emoji; monospace for code and the trace panel; light and dark; Mantine 7". Record the chosen direction (palette tokens, type scale, spacing) in `app/src/theme.ts` comments.**
- [ ] **Step 2: Write failing tests in `app/test/webauthn.test.ts`**

```ts
test('toCreationOptions decodes challenge, user.id and excludeCredentials ids to ArrayBuffers of the right length', ...)
test('toRequestOptions decodes challenge and allowCredentials ids', ...)
test('serializeCredential(attestation fixture as a PublicKeyCredential-like object) round-trips to the fixture JSON', ...)   // build the input by decoding the fixture, serialize, deepEqual
test('serializeCredential on an assertion includes userHandle null when absent', ...)
```

- [ ] **Step 3: Run `pnpm --filter app test`** — FAIL. **Step 4: Implement.** **Step 5: Run** — PASS; `pnpm run check` green.
- [ ] **Step 6: Commit** — `feat(app): shell, theme, api client, webauthn codec and trace drawer`

---

### Task 7: app demo screens — the three scenarios and enrollment

**Files:**
- Create: `app/src/app/session.tsx`, `Register.tsx`, `Login.tsx`, `SecondFactor.tsx`, `PasskeyLogin.tsx`, `Home.tsx`, `Security.tsx`, `StepUpModal.tsx`, `app/src/app/index.tsx` (nested routes)
- Modify: `app/src/routes.tsx`

**Interfaces:**
- Consumes: Task 6 `api`, `toCreationOptions`, `toRequestOptions`, `serializeCredential`, `ApiError`.
- Produces: `useMe()` → `{ me: {username, email, factors} | null; status: 'loading' | 'anon' | 'half' | 'full'; reload() }` from `GET /api/me` (401 `second_factor_required` → `half`, 401 `unauthorized` → `anon`); `SessionGuard` redirects `anon` → `/app/login`, `half` → `/app/login/second-factor`.
  Screens per spec §5.1. `SecondFactor` offers PIN and passkey buttons based on `factors` from login, calls `second-factor/start` then `verify`; on `verification_failed` shows "Wrong PIN, {attemptsRemaining} attempts left"; `locked` disables the form and shows `lockedUntil`; `challenge_gone` restarts automatically once. `PasskeyLogin`: username → `passkey/start` → `navigator.credentials.get` → `passkey/verify`; `factor_not_enrolled` → message with a link to `/app/login`. `Home`: greeting, email, "Change" opens `StepUpModal` with `purpose 'change-email'`; on success the modal returns `stepUpToken` and `Home` calls `POST /api/me/email {email, stepUpToken}`; `step_up_required` → "Verification expired, try again". `Security`: set/remove PIN (two inputs, must match), add passkey (`me/passkeys/start` → `navigator.credentials.create` → `finish`), list and remove passkeys; `registration_failed` → "Could not register this passkey".

- [ ] **Step 1: Implement the screens.** No unit tests (spec §7); the proof is Step 2.
- [ ] **Step 2: HUMAN — with `bff` running (Task 5 Step 5) and `pnpm --filter app dev`, in a browser with a platform authenticator: register → security: set PIN, add passkey → logout → login with password + PIN → logout → passkey login → change email with step-up by passkey. The trace drawer must show every auth-gate call. Record any deviation in the handoff.**
- [ ] **Step 3: `pnpm run check`** — green. **Step 4: Commit** — `feat(app): register, login with second factor, passkey login, security and step-up`

---

### Task 8: app docs — loader, layout, language toggle, the eight pages in EN and VI

**Files:**
- Create: `app/src/docs/loader.ts`, `DocsLayout.tsx`, `DocsPage.tsx`, `docs/en/{what-it-is,setup,enroll,2fa-login,passkey-login,step-up,verify-token,errors}.md`, the same eight under `docs/vi/`
- Modify: `app/src/routes.tsx`, `app/vite.config.ts` (`server.fs.allow` includes `../docs`)

**Interfaces:**
- Produces:
  ```ts
  type DocPage = { slug: string; lang: 'en' | 'vi'; title: string; tryIt?: string; html: string }
  const pages: Record<'en' | 'vi', DocPage[]>   // import.meta.glob('../../../docs/*/*.md', {query: '?raw', import: 'default', eager: true}); order = the eight slugs above
  function parseFrontmatter(raw: string): { title: string; tryIt?: string; body: string }   // `---\ntitle: …\ntryIt: …\n---`; no library
  ```
  `DocsLayout`: left nav (eight titles in the current language), header with EN/VI toggle and a "Demo" link to `/app/login`; `DocsPage` renders `marked(body)` (GFM, `mangle`/`headerIds` off) and, when `tryIt` is set, a "Try it" button linking to `${tryIt}?trace=1`. Code blocks get a copy button. Unknown slug → `/docs/what-it-is`.
- Content rules (both languages, same structure): each page fits one screen at 1280×800; no page over 60 lines of Markdown; sequence diagrams as fenced ```text blocks; backend snippets are TypeScript taken from `bff/src` (the relay call and `token.ts`), browser snippets from `app/src/lib/webauthn.ts`; EN is written first, VI is a translation that keeps the code identical. Page contents per spec §5.3; `errors` reproduces spec §4.7 as a table; `step-up` includes the paragraph on why the token travels with the action and the sentence that a replayed token is accepted until `exp` in v1 (track `jti` if that matters to you). `setup` shows the `02-register-client.sh` call and the `curl` behind it.

- [ ] **Step 1: Implement loader, layout and page; `pnpm run check`** — green with one placeholder page.
- [ ] **Step 2: Write the eight EN pages, then the eight VI pages. Read each aloud once: if it sounds like an announcement or a memo, cut it.**
- [ ] **Step 3: HUMAN — open `/docs/step-up`, press "Try it", confirm the demo opens with the drawer open; switch to VI, confirm the same page in Vietnamese.**
- [ ] **Step 4: Commit** — `feat(docs): docs pages in en and vi with try-it links into the demo`

---

### Task 9: infra — SAM template, deployer policy, deploy scripts

**Files:**
- Create: `infra/template.yaml`, `infra/deployer-policy.json`, `scripts/setup-and-deploy/00-deployer.sh`, `01-ssm.sh`, `02-build.sh`, `03-deploy.sh`, `99-purge.sh`

**Interfaces:**
- Produces: stack `auth-app-<stage>` per spec §6: `Table` (pk/sk, on-demand); `Bff` (`AWS::Serverless::Function`, `nodejs22.x`, arm64, 256 MB, 10 s, `CodeUri bff/dist`, `Handler index.handler`, `FunctionUrlConfig AuthType NONE`, env `STAGE TABLE AUTHGATE_BASE_URL AUTHGATE_CLIENT_ID TRACE`, policies: `dynamodb:GetItem/PutItem/UpdateItem/TransactWriteItems` on `Table`, `ssm:GetParameter` on `/auth-app/<stage>/*`, `kms:Decrypt` on the SSM default key); `SiteBucket` (private, `BucketEncryption` AES256) with an `AWS::CloudFront::OriginAccessControl` and a bucket policy allowing only the distribution; `Distribution`: origin `api` = Function URL host with `OriginCustomHeaders x-origin-verify = {{resolve:ssm:/auth-app/<stage>/origin-verify}}`, origin `site` = the bucket; behaviour `/api/*` → `api` with managed `CachingDisabled` + `AllViewerExceptHostHeader`, allowed methods all; default → `site` with `CachingOptimized`; `CustomErrorResponses` 403 and 404 → `/index.html` 200; `redirect-to-https`, `http2and3`, `PriceClass_200`. Parameters `Stage`, `AuthGateBaseUrl`, `AuthGateClientId` (default `""`), `Trace` (default `"1"`). Outputs `BaseUrl`, `RawBffUrl`, `SiteBucket`, `DistributionId`.
- `deployer-policy.json`: CloudFormation, Lambda, IAM role pass/create for `auth-app-*`, S3 on `auth-app-*`, CloudFront (create/update/invalidate), DynamoDB on `auth-app-*`, SSM on `/auth-app/*`, logs; copied in shape from auth-gate's and narrowed to these services.
- Scripts (all source `lib.sh`): `00-deployer.sh` creates the IAM user and local profile (`ADMIN_PROFILE` env, as auth-gate); `01-ssm.sh` writes random `session-secret` (SecureString) and `origin-verify` (String), refusing to overwrite; `02-build.sh` = `pnpm -r build` then checks `bff/dist/index.mjs` and `app/dist/index.html` exist; `03-deploy.sh` = `sam deploy --parameter-overrides Stage AuthGateBaseUrl AuthGateClientId Trace` (`AUTHGATE_CLIENT_ID` env, may be empty on the first pass), then `aws s3 sync app/dist s3://<SiteBucket> --delete`, then `create-invalidation /*`, prints `BaseUrl`; when `AUTHGATE_CLIENT_ID` is empty it prints "now run 02-register-client.sh stg and deploy again". `99-purge.sh` empties the bucket, deletes the stack and the three parameters after a typed confirmation.

- [ ] **Step 1: Write the template, policy and scripts.** `sam validate --lint -t infra/template.yaml` — valid; `pnpm run check` — green (shellcheck covers the scripts).
- [ ] **Step 2: HUMAN — `ADMIN_PROFILE=default 00-deployer.sh`; `01-ssm.sh`; `02-build.sh`; `AUTHGATE_BASE_URL=https://d2x14lcxloom8y.cloudfront.net 03-deploy.sh` (first pass); `AUTHGATE_ADMIN_KEY=… 02-register-client.sh stg`; `AUTHGATE_CLIENT_ID=<printed> 03-deploy.sh` (second pass). Check: `curl -i $RawBffUrl/api/nope` → 401; `curl -i $BaseUrl/api/nope` → 404 JSON; `curl -i $BaseUrl/docs/step-up` → 200 HTML.**
- [ ] **Step 3: Commit** — `feat(infra): sam stack with bff, site bucket and one cloudfront door; deploy scripts`

---

### Task 10: Bruno collection and `04-smoke.sh`

**Files:**
- Create: `bruno/opencollection.yml`, `bruno/environments/local.example.yml`, `bruno/01-auth/` (register, login, second-factor start+verify with PIN, logout), `bruno/02-me/` (set pin, factors, delete pin), `bruno/03-stepup/` (start, verify, change email), `scripts/setup-and-deploy/04-smoke.sh`

**Interfaces:**
- Collection `auth-app (sample client of auth-gate)`; variables `baseUrl`, `username`, `password`, `pin`, `challengeId`, `stepUpToken`; cookies handled by the Bruno cookie jar; every request has `docs:` (why, expected status) and `tests` assertions; `after-response` scripts capture `challengeId` and `stepUpToken`. `local.example.yml`: `baseUrl http://localhost:8787`, `username smoke-<random>` set by a collection `pre-request` script when empty.
- `04-smoke.sh`: reads `BaseUrl` from the stack outputs, runs `npx -y @usebruno/cli run bruno --env stg` (env file written from the output), then the two curl checks of Review Focus 5 (`/docs/step-up` → 200 and `content-type: text/html`; `/api/does-not-exist` → 404 and body `{"error":"not_found"...}`), exits non-zero on any failure.

- [ ] **Step 1: Write the collection; run it against the local `bff`** — `npx -y @usebruno/cli run bruno --env local` — Expected: all pass (the PIN path does not need a browser).
- [ ] **Step 2: HUMAN — `04-smoke.sh` against stg** — Expected: Bruno green, both curl checks pass.
- [ ] **Step 3: Commit** — `feat(bruno): sample-client collection and stage smoke`

---

### Task 11: README, CONTRIBUTING, handoff

**Files:**
- Create: `CONTRIBUTING.md`
- Modify: `README.md`, `CLAUDE.md`

**Interfaces:**
- `README.md` (English, short): what the site is and the stg URL; the diagram from spec §3; "run locally" (docker compose, `02-register-client.sh local`, `pnpm dev`); "deploy" as the numbered script list with the two-pass note; "what to copy" pointing at `bff/src/authgate/token.ts`, `bff/src/authgate/client.ts`, `app/src/lib/webauthn.ts`; layout block. `CONTRIBUTING.md`: Node 22, pnpm 10.13.1, `pnpm run check`, where tests live and why only there. `CLAUDE.md`: first paragraph describes auth-app and the FE ↔ `/api` contract.

- [ ] **Step 1: Write them; `pnpm run check`** — green.
- [ ] **Step 2: Commit** — `docs: readme and contributing for auth-app`
- [ ] **Step 3: Write the handoff (`/handoff`), set the BOARD row to `review`.** Then, in the auth-gate repository (separate worktree, separate plan row): add one README line linking to the auth-app site.
