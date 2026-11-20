# Infra hardening — plan

Spec: `docs/superpowers/specs/2026-11-20-infra-hardening-design.md`. One task: the changes are
small and share one rollout. No step touches AWS; the human runs the rollout.

**Done when:** `sam validate --lint` passes on both templates; the policy is valid JSON under 6144
non-whitespace characters; `shellcheck -x` is clean; the stubbed `99-purge.sh` test covers the
branches below; `pnpm run check` is green; the docs carry the rollout and the proofs.

## Task 1: protect the table, narrow the deployer

**Files:** `infra/template.yaml`, `infra/console.yaml`, `infra/deployer-policy.json`,
`scripts/setup-and-deploy/{03-deploy.sh,99-purge.sh}`, `scripts/console/deploy.sh`,
`README.md` (deploy and permissions-boundary sections), shell tests next to the existing ones.

- [ ] **Step 1: table.** `DeletionProtectionEnabled: true` on the AuthGate table. `99-purge.sh`:
  after the typed confirmation and before `delete-stack`, run
  `aws dynamodb update-table --deletion-protection-enabled false` (it is `--no-deletion-protection-enabled`
  in the CLI: check) and wait for the table to be `ACTIVE`; an absent table (`ResourceNotFoundException`)
  is skipped with a message; any other error stops the script. Copy the shape from
  `scripts/console/purge.sh`.
- [ ] **Step 2: S3 prefix.** Add `--s3-prefix "$STACK"` to both `sam deploy` calls. Policy: replace
  `s3:*` on the SAM bucket with the object actions SAM and CloudFormation use (`PutObject`,
  `GetObject`, and whatever else, each with a reason) on `<bucket>/auth-gate-*`, and the bucket-level
  actions SAM needs on the bucket (`ListBucket` limited by `s3:prefix` if it works, `GetBucketLocation`,
  others with a reason). Read how `sam deploy --resolve-s3` finds and uses the bucket; state how sure
  you are. CloudFormation reads the template from S3 with the caller's rights.
- [ ] **Step 3: KMS.** Per the spec, decision 3. Read AWS KMS's docs on ABAC and on tagging at
  creation (`CreateKey` with `Tags`): which actions are authorised, on which resource, and whether
  `aws:ResourceTag` or only `aws:RequestTag` applies. A `TagResource` on an existing key must require
  the resource tag, so a foreign key cannot be tagged into scope.
- [ ] **Step 4: CloudFront.** Tag both distributions `app = auth-gate` in the templates (or by stack
  tags; say which, and check CloudFormation propagates them). Check in the service reference which
  CloudFront actions take `aws:ResourceTag` and `aws:RequestTag`; scope those, leave the rest on `*`
  with the reason in your report.
- [ ] **Step 5: logs.** Per the spec, decision 5.
- [ ] **Step 6: docs.** README: the rollout order from spec section 3 and the four proofs as
  paste-ready commands, read-only. Use `simulate-principal-policy` with concrete ARNs; where an ARN
  is unknown to you (another project's key or distribution), take it from a read-only `list-*` call
  in the same snippet.
- [ ] **Step 7: checks.** The done list above. Stub `aws` in `PATH` with sandbox files for
  `99-purge.sh`: wrong confirmation (nothing called), table present, table absent, update-table
  failing for another reason. Never touch `~/.aws`.
- [ ] **Step 8: commit.** `feat(infra): auth-gate table protected from deletion; deployer scoped to auth-gate's own s3, kms, cloudfront and logs`
