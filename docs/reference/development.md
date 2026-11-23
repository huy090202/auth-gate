# Development

## Local development

You need Go (`go 1.26.0` per `go.mod`), Docker, Node.js 22 or newer and pnpm. Put
golangci-lint v2 on your `PATH` (`$(go env GOPATH)/bin`).

```bash
make dev    # DynamoDB Local on :8000 (docker compose) and all roles on :8080
```

`make dev` serves the three roles on one port under the prefixes `/verify`, `/admin` and
`/jwks`, with an in-memory signer and the admin key `dev-admin`. The Bruno collection
adds the prefix itself when `baseUrl` is a localhost URL. Run it from `bruno/`:

```bash
cd bruno/environments && cp local.example.yml local.yml
cd ../.. && pnpm install   # once: installs the pinned Bruno CLI
cd bruno && ../node_modules/.bin/bru run --env local --sandbox=developer
```

`make check` runs `go vet`, `golangci-lint run` and `go test ./...`; `make test-store`
also runs the store tests against DynamoDB Local.

## Worktree tooling

This repository is developed as a bare clone with one folder per branch. The scripts in
`tools/wt/` and the workflow are documented in [`docs/infra/`](../infra/00-overview.md).
A plain `git clone` also works for contributing.

## Gate and tooling

You need Node.js 22 or newer, pnpm 10.13.1, `jq` and `git`.

If corepack's pnpm shim fails with "Cannot find matching keyid", install pnpm directly
(see `docs/infra/05-pnpm.md`):

```bash
corepack disable pnpm
npm install -g pnpm@10.13.1
```

Then:

```bash
pnpm install
pnpm run check   # lint, shell tests, then make check (Go)
```
