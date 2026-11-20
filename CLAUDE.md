# auth-gate

auth-gate is a two-factor verification service (PIN and WebAuthn passkeys) that other
systems call backend to backend with a per-client key. Go, on AWS Lambda: `cmd/auth-gate`, `internal/auth`, `infra/template.yaml`.

- AuthGate is the verification service: three roles in one binary behind one CloudFront door.
- The Console is the operator's web app for managing clients: `cmd/console`, `internal/console`, `console-web/`, `infra/console.yaml`. Start with `README.md` and `docs/memo/console-sessions.md`.

## Rules

- Code, comments, docs and commit messages are in English.
- If `local/rules.md` exists, read it. It takes precedence over this file.
- Commit only when asked. Use Conventional Commits.
- Never push `main` directly. `tools/wt/merge.sh` does that, after the gate passes.
- `pnpm run check` (lint, shell tests, `make check`) must pass before you call work done.

## Workspace layout

- The workspace root holds `.bare/` (the git repository) and one folder per worktree.
- `main/` is branch `main`; each feature is `feat-<name>/` on branch `feat/<name>`.
- `orchestrator/` is an orphan branch holding `BOARD.md` and `handoffs/`. Read it via `../orchestrator/`.
- `local/` is a symlink to `../local`: private, gitignored notes and secrets.
- Details: `docs/infra/00-overview.md`.

## Session continuity

- Your BOARD row and latest handoff were injected at session start. If they were not,
  read `../orchestrator/BOARD.md` and the newest file in `../orchestrator/handoffs/<dir>/`.
- Run `/handoff` before you stop, and as soon as the context guard warns you.
