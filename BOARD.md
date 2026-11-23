# Board

| Worktree | Branch | Task | Status | Last handoff | Notes |
|---|---|---|---|---|---|
| feat-infra-tooling | feat/infra-tooling | Worktree scripts, hooks, pnpm skeleton, CI, docs (round 1) | merged | 2026-10-18-1640 | plan: docs/superpowers/plans/2026-10-18-worktree-infrastructure.md |
| feat-smoke | feat/smoke | Smoke test: add one line to README | merged | 2026-10-18-1757 | trial run of worktree flow |
| docs-verified | docs/verified | Record interactive verification results in docs/infra | merged | 2026-10-18-1810 | |
| feat-auth-gate | feat/auth-gate | auth-gate module v1: Go, 3 roles behind one CloudFront URL, PIN + WebAuthn, KMS tokens, Bruno (round 2) | merged | 2026-10-20-1117 | spec+plan in handoffs/feat-auth-gate/; 15 tasks; subagent-driven |
| feat-merge-commits | feat/merge-commits | merge.sh: --no-ff merge commits (semi-linear history) | merged | 2026-10-19-1037 | |
| feat-rm-after-rebase | feat/rm-after-rebase | rm.sh: remove rebased+merged branches without --force | merged | 2026-10-20-1129 | brief in handoffs/feat-rm-after-rebase/ |
| feat-admin | feat/admin | AuthGate Console and per-client rate limit | merged | plan.md | spec and plan in handoffs/feat-admin/; PRs #2, #3, #4 merged (0e31ef4, 2026-11-20); worktree still present |
| feat-infra-hardening | feat/infra-hardening | Infra hardening: permissions boundary on every deployer-made role, deployer scope, reserved concurrency opt-in | merged | | PR #5 merged (55fcdcf, 2026-11-20); worktree still present |
| feat-ci-deploy | feat/ci-deploy | Continuous deploy on merge to main through a GitHub OIDC role (no stored key) | merged | | PRs #6, #7 merged (8661ea7, 2026-11-20); worktree still present |
| feat-self-service | feat/self-service | Self-service P1 (tenant-aware AuthGate, portal key) and P2 (the Portal: accounts, workspaces, clients) | merged | | PRs #8, #9 merged (180e20e, 2026-11-22); P3 organisations and P4 operator screens later; worktree still present |
| feat-custom-domain | feat/custom-domain | authgatehq.com for AuthGate (api.), Console (admin.), Portal (portal.); one ACM cert; SES domain identity with DKIM/DMARC | merged | | PR #10 merged (50abe95, 2026-11-23); rollout done on stg; SES production access granted 2026-11-23; worktree still present |
| fix-web-kit-flaky-test | fix/web-kit-flaky-test | Unmount the step-up test's React root (flaky CI: window is not defined) | merged | | PR #11 merged (26a8518, 2026-11-23); worktree still present |
| feat-portal-email | feat/portal-email | Branded HTML Portal email with a text part, Reply-To support@, Cloudflare Email Routing, DMARC rua | merged | | PR #12 merged (985e13a, 2026-11-23); rollout done on stg; worktree still present |
| fix-ci-shellcheck-pin | fix/ci-shellcheck-pin | CI passes its token to the shellcheck wrapper (GitHub API 403 rate limit) | merged | | PR #13 merged (5b56cf0, 2026-11-23); its release pin had no effect and was removed in PR #15; worktree still present |
| feat-readme | feat/readme | README for first-time readers; reference to docs/reference, rollouts to docs/runbooks | merged | | PR #14 merged (3f8f0a9, 2026-11-23); worktree still present |
| fix-post-domain-docs | fix/post-domain-docs | Drop the dead shellcheck pin; SES production-access text as sent; README demo link to the apex | merged | | PR #15 merged (7f8c8c4, 2026-11-23); worktree still present |
| feat-stg-reset | feat/stg-reset | Script to reset stg user data (dry run, --apply, typed confirmation); keeps Console admins, app clients, stacks | active | | handoff in auto memory: handoff-feat-stg-reset; next: spec for Huy's review |

Status values: planned, active, review, merged, abandoned.
