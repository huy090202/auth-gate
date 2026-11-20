# Continuous deploy to stg, with GitHub OIDC

Date: 2026-11-20. Status: approved by the product owner in chat the same day, section by section.

## 1. Purpose

Every merge to `main` is deployed to stg by hand today: a person runs the build, deploy and
smoke scripts for AuthGate and the Console. This change makes GitHub Actions do it on every
push to `main`, with short-lived AWS credentials from OpenID Connect (OIDC) and no stored key.
It also expires old SAM artifacts, which a deploy per merge would otherwise pile up.

Decided by the product owner (not re-askable):

- OIDC, not an access key in GitHub Secrets.
- stg only. Deploys run on push to `main`, never on a pull request.
- After deploying, CI runs both smoke scripts, as a person does today. The role therefore reads
  `admin-key` from SSM, as the deployer already does.
- The role is created by an admin script, in the style of `00-deployer.sh`, and carries the
  deployer's own managed policy. One policy file serves both.
- Old SAM artifacts expire after 30 days.

## 2. Workflow

`.github/workflows/ci.yml` gains a job `deploy`:

- `needs: check`, and `if: github.event_name == 'push' && github.ref == 'refs/heads/main'`.
- Job permissions `id-token: write` and `contents: read`. The workflow default stays
  `contents: read`, so no other job can request a token.
- `concurrency: { group: deploy-stg, cancel-in-progress: false }`. Deploys queue. GitHub keeps
  one pending run per group, so the newest merge wins.
- The workflow-level concurrency stays grouped by ref, with
  `cancel-in-progress: ${{ github.event_name == 'pull_request' }}`. Today it cancels on every
  ref, so a second merge would kill the first deploy in the middle of `sam deploy` and leave the
  stack in `UPDATE_IN_PROGRESS`.

Steps:

1. Check out the code; set up pnpm, Node 22, Go (from `go.mod`), the SAM CLI; `pnpm install
   --frozen-lockfile`.
2. `aws-actions/configure-aws-credentials` with `role-to-assume:
   arn:aws:iam::826113468815:role/ci-auth-gate-deploy`, `aws-region: ap-southeast-1` and a
   session name carrying the run id. The ARN is not a secret: the account id is already in
   `infra/deployer-policy.json`.
3. `scripts/ci/preflight.sh`: both `auth-gate-stg` and `auth-gate-console-stg` exist, each in
   `CREATE_COMPLETE`, `UPDATE_COMPLETE` or `UPDATE_ROLLBACK_COMPLETE`, and the Console's
   `AuthGateClientId` output is set. Otherwise it stops with a message saying a first deploy or a
   recovery is done by hand, under admin. CI never creates a stack.
4. `scripts/setup-and-deploy/02-build.sh`, `03-deploy.sh`, `04-smoke.sh`, then
   `scripts/console/deploy.sh` and `scripts/console/smoke.sh`. These are the scripts a person
   runs, unchanged apart from item 5.
5. `scripts/setup-and-deploy/lib.sh` and `scripts/console/lib.sh` default `AWS_PROFILE` to
   `auth-gate-deployer` only when `AWS_ACCESS_KEY_ID` is unset. A named profile that does not
   exist makes the AWS CLI fail even when credentials are in the environment, which is how the
   OIDC step hands them over. On a workstation nothing changes.

Third-party actions in the `deploy` job (`aws-actions/configure-aws-credentials`,
`aws-actions/setup-sam`, and those it shares with `check`) are pinned by full commit SHA, with
the tag in a comment. This job holds AWS credentials, so a moved tag must not change its code.

Failures:

- `sam deploy` fails: CloudFormation rolls the stack back; the job is red.
- A smoke fails after a successful deploy: the new code is live on stg. Fix forward, or revert
  the PR, since the revert's merge deploys too.
- GitHub emails the pusher when a workflow fails. Nothing else notifies.

## 3. Role and trust

`scripts/ci/00-ci-role.sh`, run by an admin (`ADMIN_PROFILE=default`), safe to rerun:

1. Find the OIDC provider `token.actions.githubusercontent.com` in the account. If absent,
   create it with client id `sts.amazonaws.com`. If present (another project may own it), use it
   unchanged.
2. Create the role `ci-auth-gate-deploy` with the trust policy in `infra/ci-trust.json`, the
   provider ARN filled in. If it exists, replace its trust policy with the file's.
3. Attach the managed policy `auth-gate-deployer-policy`. If that policy does not exist, stop:
   `00-deployer.sh` has not run.
4. Print the role ARN.

The trust policy allows `sts:AssumeRoleWithWebIdentity` only when, with `StringEquals` and no
wildcard:

- `token.actions.githubusercontent.com:aud` is `sts.amazonaws.com`, and
- `token.actions.githubusercontent.com:sub` is `repo:huy090202@100850219/auth-gate@1423990644:ref:refs/heads/main`.
  GitHub's `sub` carries the owner and repository IDs (`100850219`, `1423990644`) after each
  name, so a renamed or re-created `huy090202/auth-gate` does not match. The first CI run showed
  this form (CloudTrail, 2026-11-20); the plain `repo:huy090202/auth-gate:…` form was refused.

A pull request (`…:pull_request`), a push to any other branch, a fork, and every other
repository are refused.

Why the role cannot raise its own rights: its name does not match `role/auth-gate-*`, the only
roles the deployer policy can change, so it cannot rewrite its trust, attach a policy or delete
itself. It cannot create users or access keys. A session lasts one hour, the default.

Residual risk: any workflow on `main` can assume the role, so a PR that changes
`.github/workflows/` or `scripts/` runs with deploy rights once merged. Review of PRs to `main`
is the control. Binding the role to one workflow file (`job_workflow_ref`) needs GitHub's OIDC
claim customisation and is not done while only the owner merges.

## 4. Lifecycle rule

`scripts/ci/00-sam-lifecycle.sh`, run once by an admin:

1. Read the bucket name from the `SourceBucket` output of the stack
   `aws-sam-cli-managed-default`, the bucket `sam deploy --resolve-s3` uses.
2. Read the bucket's lifecycle configuration. `NoSuchLifecycleConfiguration` means none; any
   other error stops the script before it writes anything.
3. Keep every rule whose `ID` does not start with `auth-gate-artifacts-`. Replace those that do
   with the two below. `put-bucket-lifecycle-configuration` replaces the whole configuration, so
   the merge is what keeps other projects' rules.
4. Print the rule IDs before and after, then write.

The rules, both filtered on prefix `auth-gate-` (it covers `auth-gate-stg/` and
`auth-gate-console-stg/`):

- `auth-gate-artifacts-expire`: `Expiration.Days 30`, `NoncurrentVersionExpiration.NoncurrentDays
  7`, `AbortIncompleteMultipartUpload.DaysAfterInitiation 7`. The SAM-managed bucket normally has
  versioning on, so expiring a current object leaves a noncurrent version and a delete marker.
- `auth-gate-artifacts-markers`: `Expiration.ExpiredObjectDeleteMarker true`. S3 does not allow
  it in the same rule as `Expiration.Days`.

The rollback trap: rolling back a failed update of a Lambda function reads the previous code zip
from S3. If stg has not been deployed for more than 30 days, that zip has expired, and the next
deploy fails, the rollback fails too and the stack stops in `UPDATE_ROLLBACK_FAILED`. Each build
zips with fresh file times, so a deploy always uploads new objects and the window opens only
after 30 idle days. Recovery: `aws cloudformation continue-update-rollback --stack-name <stack>
--resources-to-skip <logical id>`, then deploy again. The README carries this.

Out of scope: artifacts at the bucket root from before the S3 prefix. Nothing tells auth-gate's
apart from other projects'.

## 5. Rollout

The merge of this branch is the first run of the `deploy` job, so the role must exist first.

1. Before the merge, from the `feat-ci-deploy` worktree, with the admin profile:
   `scripts/ci/00-ci-role.sh` and `scripts/ci/00-sam-lifecycle.sh`.
2. Merge. CI runs `check`, then `deploy` with both smokes. A green run proves the role, the
   trust, the policy and the `lib.sh` change.
3. Proofs, read-only, as admin: `aws iam get-role` shows the trust policy above;
   `aws iam list-attached-role-policies` shows `auth-gate-deployer-policy`;
   `aws s3api get-bucket-lifecycle-configuration` shows both `auth-gate-artifacts-*` rules and
   every rule that was there before.

## 6. Tests and checks

- A stub-`aws` test for `00-sam-lifecycle.sh`, in the style of
  `scripts/setup-and-deploy/test/99-purge.sh`: no configuration yet; other projects' rules
  (kept as they are); older `auth-gate-artifacts-*` rules (replaced); a read failing for another
  reason (nothing written). This is the one place a bug deletes someone else's data.
- `actionlint` joins `pnpm run lint`, at a pinned version. The `deploy` job runs only on
  `main`, so without it a broken workflow is found only after the merge.
- `shellcheck -x` covers `scripts/ci/*.sh` and the new test.
- No test for `00-ci-role.sh` or the `lib.sh` change: the first CI deploy exercises both.
- `pnpm run check` passes.

## 7. Docs

README gains a section "Continuous deploy":

- What runs, and when.
- Setting up the role and the lifecycle rule.
- The `UPDATE_ROLLBACK_FAILED` recovery.
- What stays manual: `00-deployer.sh`, a stage's first deploy, purges, `register-client.sh`,
  `05-backfill-clients.sh`.
- Ordering when a PR changes rights. A template that needs a new right: run `00-deployer.sh`
  from the branch before merging, or the CI deploy fails. A policy that narrows rights: merge
  first, then apply, as in the infra-hardening rollout.

The sentence "Keep the deployer key out of CI and shells you do not control" changes: CI uses
the role, not the key.

## 8. Non-goals

A production stage; manual approval before a deploy; preview stacks per PR; `job_workflow_ref`;
retiring the deployer's access key (manual deploys still use it); deleting artifacts at the
bucket root; notifications beyond GitHub's default email.

Cost: AWS, nothing measurable (STS is free; storage is cents). GitHub Actions: free for a public
repository; a private one has 2000 free minutes a month, and a merge uses about 10.
