# Brief: feat/auth-app (repository huy090202/auth-app)

## Goal
Build auth-app v1: one site at one URL that is both the integration docs for auth-gate
(EN/VI) and a working sample client backend + frontend (password + PIN + passkey, the
three auth-gate scenarios), deployed on AWS next to auth-gate stg.

## Constraints
- Follow `docs/superpowers/plans/2026-10-20-auth-app.md` task by task with
  superpowers:subagent-driven-development (chosen by the human on 2026-10-20).
- Spec: `docs/superpowers/specs/2026-10-20-auth-app-design.md`. The spec and plan come
  from the auth-gate orchestrator (`handoffs/auth-app/`); Task 0 commits them here.
- UI direction via the `impeccable` skill, not `frontend-design`.
- Reference for patterns, read only: `/home/work/freelancer/infinity/fix-guarantee-badges/alpha`
  (hub BFF/app) and `/home/work/auth-gate/main` (SAM, scripts, CloudFront door).
- Steps marked HUMAN are run by the human (AWS, SSM, registering clients on auth-gate).
- auth-gate stg base URL: `https://d2x14lcxloom8y.cloudfront.net`. Never run auth-gate locally.

## Definition of done
- Every plan checkbox ticked, `pnpm run check` green, branch pushed.
- Stack `auth-app-stg` deployed; `04-smoke.sh` green; manual browser run of Task 7 Step 2 done.
- Handoff written with an empty "Remaining".

## References
- Spec and plan: `docs/superpowers/specs/2026-10-20-auth-app-design.md`, `docs/superpowers/plans/2026-10-20-auth-app.md`
- auth-gate README and Bruno collection: `/home/work/auth-gate/main/README.md`, `/home/work/auth-gate/main/bruno/`
