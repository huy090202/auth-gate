# Deploy and CI

## Deploy

The deploy and smoke steps also run in CI on every merge to `main`
([Continuous deploy](#continuous-deploy)). A person runs them for first deploys, recoveries and
other stages, with AWS access. They live in `scripts/setup-and-deploy/` and read `STAGE` (default `stg`), `AWS_PROFILE` (default
`auth-gate-deployer`) and `AWS_REGION` (default `ap-southeast-1`):

0. `00-deployer.sh` (once per account and after changing either JSON, with an admin profile:
   `ADMIN_PROFILE=default`): create or update the permissions boundary `auth-gate-boundary` from
   `infra/role-boundary.json`, then the IAM user `auth-gate-deployer` with
   the two managed policies, scoped to `auth-gate-*` resources, and a local AWS profile of
   the same name. The deployer's rights are split over two files because one managed policy
   holds at most 6144 characters: `auth-gate-deployer-policy` from `infra/deployer-policy.json`
   (CloudFormation, S3, Lambda, the IAM roles under the boundary, DynamoDB, KMS, SSM and logs)
   and `auth-gate-deployer-edge-policy` from `infra/deployer-policy-edge.json` (CloudFront
   distributions and policies, edge functions, the ACM certificate rights). Both are
   attached to the user and to the CI role, and together they are the deployer's rights. The deployer cannot create the stacks: a stage's first deploy runs under the
   admin profile ([First deploy of a stage](#first-deploy-of-a-stage)).
1. `00-cert.sh DOMAIN [--wait]` (optional): request the ACM certificate for `DOMAIN` and `*.DOMAIN` in `us-east-1`, record `domain` and `certificate-arn` in SSM, and print the DNS validation record. `03-deploy.sh` then deploys AuthGate on `api.DOMAIN` once the certificate is issued. The rollout is in [the custom-domain runbook](../runbooks/rollout-custom-domain.md).
2. `01-ssm.sh`: write the random `admin-key` and `portal-key` (SecureStrings) and
   `origin-verify` (plain String; CloudFront cannot resolve a SecureString). A parameter that
   exists is kept, and no value ever goes on an argv.
3. `02-build.sh`: cross-compile `bin/bootstrap.zip` for linux/arm64.
4. `03-deploy.sh`: deploy the SAM stack `auth-gate-<stage>` and print the one `BaseUrl`. The
   first deploy of a stage runs under the admin profile; it will fail under the deployer
   ([First deploy of a stage](#first-deploy-of-a-stage)).
   Without a custom domain the first deploy runs twice: it reads the distribution domain,
   then redeploys to set `BASE_URL`, which avoids a CloudFormation dependency cycle.
5. `04-smoke.sh`: run the Bruno collection, then `cmd/smoke`, a real WebAuthn round trip.
6. (once, after the first deploy that has client listing) `05-backfill-clients.sh`
   (`ADMIN_PROFILE=default`): write the list-index item for clients created before it
   existed. It needs `Scan`, which no Lambda role or the deployer has, so it runs under the
   operator profile. Safe to rerun; prints counts only.
7. `99-purge.sh`: tear the stage down, after a typed confirmation. AuthGate's table has
   deletion protection (it holds every client and every user's factors), so a plain
   `delete-stack` fails; `99-purge.sh` turns it off just before the delete. Rebuilding the
   stage afterwards is a first deploy, under the admin profile.

Prerequisites: AWS CLI v2 and SAM CLI. A new AWS account may refuse the distribution with
"Your account must be verified before you can add new CloudFront resources"; only AWS Support
can lift that (open an Account and billing case), so do it before the first deploy.

If a deploy fails, CloudFormation rolls back and leaves the stack in `ROLLBACK_COMPLETE`
(or `ROLLBACK_FAILED`), which cannot be updated: delete it with
`aws cloudformation delete-stack --stack-name auth-gate-<stage>` before deploying again. A
missing deployer permission goes into the file whose statements it belongs with:
`infra/deployer-policy-edge.json` for CloudFront, edge functions and the custom domain,
`infra/deployer-policy.json` for everything else; rerun `00-deployer.sh`.
Both tables have deletion protection, which blocks a plain `delete-stack`: to clear a failed
stack, run `99-purge.sh` (AuthGate) or `scripts/console/purge.sh` (the Console), which turn it
off after their typed confirmation.

### Permissions boundary

`auth-gate-boundary` is a ceiling on what any `auth-gate-*` role can do, whatever is attached
to it. The deployer can create or change such a role only if the role carries that boundary,
so a right added to a Lambda role must also go into `infra/role-boundary.json`, followed by
`00-deployer.sh`, or the function is denied it at run time. If the boundary turns out too small,
edit that JSON and rerun `00-deployer.sh`: the new default version applies without CloudFormation.

When the boundary first reaches a stage whose stack already exists, one AuthGate deploy runs
under an admin profile. The deployer may change only roles that already carry the boundary, and
AuthGate's do not yet. If CloudFormation calls `PutRolePolicy` or `AttachRolePolicy` before it
sets the boundary, the deployer is refused; and if that update fails after any role has the
boundary, rollback must remove it, which the deployer may not do. So run, in this order:

1. `00-deployer.sh`, then wait one minute: IAM changes take a moment to apply everywhere.
2. `02-build.sh`, then `AWS_PROFILE=<admin> scripts/setup-and-deploy/03-deploy.sh`, from a tree
   whose template has the boundary.
3. `scripts/console/deploy.sh`, under the deployer. So is every later deploy.

If a deployer-run update stops at `UPDATE_ROLLBACK_FAILED` because it could not remove a
boundary, finish it with an admin profile, then deploy again:
`aws cloudformation continue-update-rollback --profile <admin> --region <region> --stack-name auth-gate-<stage>`.
Tearing down a stack whose roles never got the boundary needs an admin profile too.

### Deployer scope

The deployer's S3, KMS, CloudFront distribution and log rights reach only auth-gate's own
resources:

- **S3:** both deploys upload to their own prefix of the SAM bucket the account shares,
  `aws-sam-cli-managed-default-samclisourcebucket-m5yrn5zzyiu6` (`--s3-prefix
  auth-gate-<stage>`, `auth-gate-console-<stage>`). The deployer may read and write objects
  only under `auth-gate-*` in that bucket, and list only that prefix. Artifacts of deploys from
  before the prefix stay at the bucket root; the lifecycle rules of [Continuous deploy](#continuous-deploy) expire the new ones.
- **KMS:** every right on a key requires the tag `app = auth-gate`, including `PutKeyPolicy`,
  `TagResource` and `DescribeKey`, so tagging cannot pull another project's key into scope.
  `CreateKey` requires the request to carry that tag.
- **CloudFront:** both distributions carry `app = auth-gate`. Reading, changing, deleting,
  tagging and invalidating a distribution requires that tag; creating one requires it in the
  request. Cache, origin request, response headers and origin access control policies stay
  account-wide: CloudFront has no tag or name condition for them.
- **Logs:** reading log events only from `/aws/lambda/auth-gate-*`.

The tags, the prefix and AuthGate's table protection must be in AWS before the narrowed policy
is. Once `00-deployer.sh` applies it, a deploy from a tree without `--s3-prefix` fails at the
upload, so the first rollout runs from `main`, and every other worktree rebases on `main` before
it deploys again:

1. Merge this change to `main`. From `main`, with the old policy still applied: `02-build.sh`,
   `03-deploy.sh`, then `scripts/console/deploy.sh`. The old policy already allows the tags, the
   new prefix and the table update. If this step fails, CloudFormation rolls the update back and
   nothing else has changed yet.
2. `ADMIN_PROFILE=default 00-deployer.sh`, then wait one minute.
3. From `main`, as the deployer: `02-build.sh`, `03-deploy.sh`, `scripts/console/deploy.sh`,
   `04-smoke.sh`, `scripts/console/smoke.sh`. Both builds zip with fresh file times, so both
   deploys upload new code under their prefix and CloudFormation reads the templates from there.
   The one tag-gated write this step exercises is the Console's `create-invalidation` on its
   tagged distribution; the KMS and CloudFront writes that a change would need (`PutKeyPolicy`,
   `TagResource`, `UpdateDistribution`) are shown only by the simulator in step 4.
4. The proofs below, read-only, under the admin profile in a subshell, so it does not leak into
   later deploys. The simulator does not read the resources' tags: it evaluates the policy against
   the tag value given as context. So it runs on auth-gate's own key and distribution twice, once
   with `app = auth-gate` (expect `allowed`) and once with another value (expect
   `implicitDeny`); the real tags are read at the end.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
acct="$(aws sts get-caller-identity --query Account --output text)"
deployer="arn:aws:iam::$acct:user/auth-gate-deployer"
bucket=aws-sam-cli-managed-default-samclisourcebucket-m5yrn5zzyiu6
res() { aws cloudformation describe-stack-resource --stack-name "$1" --logical-resource-id "$2" \
  --query StackResourceDetail.PhysicalResourceId --output text; }
key="$(aws kms describe-key --key-id "$(res "auth-gate-$STAGE" SigningKey)" --query KeyMetadata.Arn --output text)"
dist="arn:aws:cloudfront::$acct:distribution/$(res "auth-gate-$STAGE" Distribution)"
sim() { aws iam simulate-principal-policy --policy-source-arn "$deployer" "$@" \
  --query 'EvaluationResults[].[EvalActionName,EvalResourceName,EvalDecision]' --output table; }
app() { printf 'ContextKeyName=aws:ResourceTag/app,ContextKeyValues=%s,ContextKeyType=string' "$1"; }

# Expect allowed.
sim --action-names kms:PutKeyPolicy kms:TagResource --resource-arns "$key" --context-entries "$(app auth-gate)"
sim --action-names cloudfront:UpdateDistribution cloudfront:TagResource --resource-arns "$dist" --context-entries "$(app auth-gate)"
sim --action-names s3:PutObject --resource-arns "arn:aws:s3:::$bucket/auth-gate-$STAGE/x"
# Expect implicitDeny: the same resources tagged for another project, and outside the prefix.
sim --action-names kms:PutKeyPolicy kms:TagResource --resource-arns "$key" --context-entries "$(app other-project)"
sim --action-names cloudfront:UpdateDistribution cloudfront:TagResource --resource-arns "$dist" --context-entries "$(app other-project)"
sim --action-names s3:PutObject --resource-arns "arn:aws:s3:::$bucket/other-project/x"

# The tags the allowed cases assume, and the table protection.
aws kms list-resource-tags --key-id "$key" --query Tags
aws cloudfront list-tags-for-resource --resource "$dist" --query Tags.Items
aws dynamodb describe-table --table-name "auth-gate-$STAGE" --query Table.DeletionProtectionEnabled
)
```

### First deploy of a stage

A deploy that creates the stacks runs under the admin profile: a new stage's first deploy, and
stg rebuilt after `99-purge.sh` and `scripts/console/purge.sh`. Under the deployer it will fail.
KMS authorises the `Tags` of `CreateKey` as `kms:TagResource`, which must come from an IAM
policy not restricted to particular keys, and its key-policy lockout check needs `PutKeyPolicy`
in the same way (commit `d06865b` hit that). The deployer holds both only on keys already
tagged `app = auth-gate`. The Console's first pass creates its tagged distribution the same
way, so it runs as admin too:

```bash
AWS_PROFILE=default scripts/setup-and-deploy/03-deploy.sh
AWS_PROFILE=default scripts/console/deploy.sh
```

Every later deploy, including the Console's second pass, runs as the deployer.

## Continuous deploy

Every push to `main` runs the `deploy` job in `.github/workflows/ci.yml` after `check` passes,
for stg only. It assumes the role `ci-auth-gate-deploy` through GitHub OIDC (no stored key),
runs `scripts/ci/preflight.sh`, then `02-build.sh`, `03-deploy.sh`, `04-smoke.sh`,
`scripts/console/deploy.sh`, `scripts/console/smoke.sh`, `scripts/portal/deploy.sh` and
`scripts/portal/smoke.sh`. The preflight stops the job unless all three stacks exist in
`CREATE_COMPLETE`, `UPDATE_COMPLETE` or `UPDATE_ROLLBACK_COMPLETE` and the Console's and the
Portal's `AuthGateClientId` output is set: CI never creates a stack, so the Portal's first deploy
is by hand ([Rollout of P2](../runbooks/rollout-p2.md#rollout-of-p2-the-portal)). The Portal deploy reads the repository
variables `PORTAL_EMAIL_FROM` and `PORTAL_TURNSTILE_SITE_KEY` (Settings, Secrets and variables,
Actions, Variables). Deploys queue (group
`deploy-stg`, never cancelled), so a second merge waits for a running deploy. GitHub keeps one pending run, so a third merge
replaces the queued second and the newest merge wins. If a smoke fails
after a successful deploy, the new code is live: fix forward or revert the PR, whose merge
deploys too.

### Set up

Once, under an admin profile, before the first merge that carries the `deploy` job:

```bash
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
ADMIN_PROFILE=default scripts/ci/00-sam-lifecycle.sh
```

`00-ci-role.sh` creates the GitHub OIDC provider if the account has none and the role, trusted
only for `repo:huy090202@100850219/auth-gate@1423990644:ref:refs/heads/main` (`infra/ci-trust.json`), with the managed
policies `auth-gate-deployer-policy` and `auth-gate-deployer-edge-policy`. `00-sam-lifecycle.sh` adds two lifecycle rules,
`auth-gate-artifacts-*`, to the shared SAM bucket and keeps every rule already there. Both are
rerunnable. The proofs, read-only, under the admin profile in a subshell:

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1
acct="$(aws sts get-caller-identity --query Account --output text)"
role=ci-auth-gate-deploy
aws iam get-role --role-name "$role" --query Role.AssumeRolePolicyDocument
aws iam list-attached-role-policies --role-name "$role" --query 'AttachedPolicies[].PolicyName'
bucket="$(aws cloudformation describe-stacks --stack-name aws-sam-cli-managed-default \
  --query 'Stacks[0].Outputs[?OutputKey==`SourceBucket`].OutputValue | [0]' --output text)"
aws s3api get-bucket-lifecycle-configuration --bucket "$bucket" --query 'Rules[].ID'
)
```

Expect the trust policy with `aud` `sts.amazonaws.com` and the `sub` above, the policies
`auth-gate-deployer-policy` and `auth-gate-deployer-edge-policy`, and both `auth-gate-artifacts-*` rule IDs next to every rule that
was there before. A green first run of the `deploy` job proves the rest.

A shell that exports `AWS_ACCESS_KEY_ID` makes the scripts use those credentials instead of the
`auth-gate-deployer` profile (`lib.sh` sets the profile only when no key is in the environment).
That is how CI hands over the role's credentials; on a workstation, unset it first.

### If a deploy gets stuck

A failed update rolls back by itself. If the rollback fails too, the stack is
`UPDATE_ROLLBACK_FAILED` and the preflight refuses it. Recover by hand under the admin profile:

```bash
aws cloudformation continue-update-rollback --stack-name auth-gate-stg \
  --resources-to-skip <LogicalId>
```

Pick the `<LogicalId>` from the stack's failed events, and repeat for `auth-gate-console-stg` if
that one is stuck. Then rerun the newest failed run (rerunning an older run after newer merges
deploys older code), or push a new commit.

A deploy can also fail when stg has been idle for more than 30 days: the lifecycle rule has then
expired the previous code zip that CloudFormation still references. Fix it the same way, by
running a fresh deploy.

### What stays manual

`00-deployer.sh`, `00-cert.sh`, `scripts/domain/ses-identity.sh`, a stage's first deploy (and the Console's and the Portal's second pass), purges,
`register-client.sh` (the Console's and the Portal's, with `--replace` for a new rpId), `05-backfill-clients.sh`, `06-prune-clients.sh` and `07-prune-factors.sh`.

### When a PR changes rights

A template that needs a new right: run `00-deployer.sh` from the branch before merging, or the CI
deploy fails. A policy that narrows rights: merge first, then apply, as in the infra-hardening
rollout.
