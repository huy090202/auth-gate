# Infra hardening: AuthGate table protection, narrower deployer

Date: 2026-11-20. Status: approved by the product owner in chat the same day.

## 1. Purpose

Two follow-ups left open by the Console build:

- AuthGate's own table `auth-gate-<stage>` can be deleted by a stack delete or a mistaken
  template change. The Console table is already protected; AuthGate's holds every client and
  every user's factors.
- `infra/deployer-policy.json` still grants rights that reach outside auth-gate: `s3:*` on the
  SAM bucket shared with other projects, `kms:PutKeyPolicy` and `kms:TagResource` on every key,
  CloudFront distribution writes on every distribution, and log reads on every group. A stolen
  deployer key could take over another project's KMS key or rewrite another project's
  distribution or deploy artifacts.

## 2. Decisions

1. `DeletionProtectionEnabled: true` on the AuthGate table. `scripts/setup-and-deploy/99-purge.sh`
   turns it off only after the typed confirmation, waits, then deletes the stack, as
   `scripts/console/purge.sh` already does. An absent table on a rerun is tolerated.
2. **S3:** both deploys upload to their own prefix in the shared SAM bucket
   (`--s3-prefix auth-gate-<stage>` and `auth-gate-console-<stage>`). The deployer gets object
   rights only under `auth-gate-*` in that bucket, plus the bucket-level reads SAM needs. The
   Console site bucket (`auth-gate-console-*`) keeps its rights: the stack owns it.
3. **KMS:** every key-level right, including `PutKeyPolicy`, `GetKeyPolicy`, `DescribeKey` and
   `TagResource`, requires `aws:ResourceTag/app = auth-gate`. `CreateKey` stays on `*` (AWS
   allows no resource there) but requires the request to tag `app = auth-gate`. Tagging cannot be
   used to pull a foreign key into scope.
4. **CloudFront:** distribution writes (`UpdateDistribution`, `DeleteDistribution`, tag changes,
   invalidations if the key allows it) require `aws:ResourceTag/app = auth-gate`, if CloudFront
   evaluates that key on those actions; creation requires the request tag. Both stacks tag their
   distribution `app = auth-gate`. Account-level policy objects (cache, origin request, response
   headers, origin access control) stay on `*` if they cannot be scoped; the report says so.
5. **Logs:** `FilterLogEvents` and `GetLogEvents` only on `/aws/lambda/auth-gate-*`;
   `DescribeLogGroups` stays on `*` if the action allows nothing narrower.
6. **Route 53 and ACM are out of scope.** The custom domain work, with DNS at Cloudflare, decides
   them next and will likely remove Route 53 entirely.

Every action and condition key is checked against AWS's machine-readable service reference
(`https://servicereference.us-east-1.amazonaws.com/v1/<service>/<service>.json`). An action that
ignores a condition key would silently stay open; an unconditional deny would break the deploy.

## 3. Rollout

The new policy must not land before the resources carry what it checks, and once it lands a
deploy from a tree without `--s3-prefix` fails at the upload. Order:

1. Merge this branch to `main`. Deploy AuthGate, then the Console, from `main` with the current
   deployer policy. This adds the tags, the S3 prefix and the table protection. The current
   policy already has `dynamodb:UpdateTable`; a failure here rolls back harmlessly.
2. `00-deployer.sh` with the admin profile, applying the new policy.
3. Deploy both again from `main` as the deployer, then both smoke scripts. This proves the
   narrowed policy still deploys. Every other worktree rebases on `main` before it deploys.
4. Proofs, read-only, as admin: `aws iam simulate-principal-policy` for `auth-gate-deployer` on
   auth-gate's own key and distribution, with `aws:ResourceTag/app` given as context, shows
   `allowed` for `kms:PutKeyPolicy` and `cloudfront:UpdateDistribution` with `auth-gate` and
   `implicitDeny` with another value, and `implicitDeny` for `s3:PutObject` outside the prefix.
   `aws dynamodb describe-table` shows `DeletionProtectionEnabled: true`.

A deploy that creates the stacks (a new stage, or a stage rebuilt after a purge) runs under the
admin profile: the deployer's `kms:TagResource` and `kms:PutKeyPolicy` are limited to tagged keys,
and `CreateKey` with tags needs them unrestricted.

## 4. Non-goals

Route 53, ACM and the custom domain; the IAM statements, already capped by the permissions
boundary; AuthGate's other tables (there are none).
