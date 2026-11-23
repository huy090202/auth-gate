# Contributing

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<optional scope>): <summary>`, for example `feat(wt): add --push to new.sh` or
`docs(infra): explain the context guard`. Common types: `feat`, `fix`, `docs`, `chore`,
`ci`, `test`, `refactor`. Write the summary in the imperative, in English.

## Branches

One feature per branch, named `feat/<name>` (or `fix/<name>`, `docs/<name>`). Branch from
`main`. `main` only moves by `merge.sh`: it rebases your branch on `main`, then merges it
with one merge commit (`--no-ff`), so history is semi-linear. Keep your branch rebased and
do not merge `main` into it.

If you use the worktree layout (`docs/infra/`), create the branch with:

```bash
../main/tools/wt/new.sh feat/<name>
```

## Go toolchain

`go.mod` says `go 1.26.0`. Install golangci-lint v2 and make sure `$(go env GOPATH)/bin`
is on your `PATH`, or `make check` cannot find it:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
make check    # go vet, golangci-lint run, go test ./...
```

## DynamoDB Local

The store tests need DynamoDB Local. Locally they skip unless `DYNAMO_ENDPOINT` is set (CI sets it), so a green `make check` alone
does not exercise them; run them before changing anything in `internal/platform/dynamo`
or the challenge and lockout code:

```bash
make test-store   # docker compose up -d dynamodb, then go test with DYNAMO_ENDPOINT=http://localhost:8000
make dev          # the same container, plus all three roles on :8080
```

CI runs the same container as a service.

## The gate

`pnpm run check` must pass before a branch is merged. It runs, in order:

- `pnpm run lint`: shellcheck over `scripts/setup-and-deploy/*.sh`, `scripts/console/*.sh`,
  `tools/wt/*.sh`, `tools/wt/test/*.sh` and the `.claude/` scripts, then eslint on `console-web/`.
- `pnpm run typecheck`: `tsc -b` on `console-web/`.
- `pnpm run test`: the shell tests, `tools/wt/test/hooks.sh` (statusline and hooks) and
  `tools/wt/test/e2e.sh` (worktree scripts, end to end), then the Console web tests.
- `pnpm run build`: `vite build` of the Console web app.
- `make check`: `go vet`, `golangci-lint run`, `go test`.

CI runs the same command on every push to `main` and on every pull request.

## The Console web app

`console-web/` is React, Vite and Mantine, with EN and VI strings in `src/strings.ts` (a missing key fails
typecheck). `pnpm --filter console-web dev` serves it on :5173 against `make console-dev`; see [docs/reference/console.md](docs/reference/console.md#run-it-locally).

- Do not add a `<style>` element or an inline `style` or `on*` attribute. The site's CSP is `style-src 'self'` with
  no `unsafe-inline`, and a nonce cannot work because CloudFront serves a static header. Mantine features that inject
  a `<style>` (responsive style props, `Grid`, `SimpleGrid`, `AppShell`, scroll locking on `Modal` and `Drawer`) are
  switched off or avoided; put rules in `src/styles.css`. `test/csp.test.tsx` fails if a screen renders a `<style>`.
- Toasts are in-house (`src/toast.tsx`). Do not add a toast package for the same reason: a dependency might inject
  styles.

## Running one test alone

```bash
bash tools/wt/test/e2e.sh     # worktree scripts, end to end, in a temp dir
bash tools/wt/test/hooks.sh   # statusline and hooks
```

Both are hermetic: they work in a temporary directory and do not touch your workspace,
your git config or `~/.claude`.
