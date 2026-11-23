# Rollout of P1 (tenant-aware AuthGate)

This branch adds the `portal-key` parameter, the portal and owner routes, soft delete, and splits
the deployer policy in two. It needs a person at an admin profile, in this order. Steps 1 and 2
run back to back from the `feat-self-service` worktree, with no merge to `main` in between:
`00-deployer.sh` rewrites `auth-gate-deployer-policy` without the CloudFront rights, which
the CI role already holds through it, and until `00-ci-role.sh` has attached
`auth-gate-deployer-edge-policy` to that role, a CI deploy would fail on CloudFront.

Before the merge:

```bash
# 1. portal-key; the other two are kept
scripts/setup-and-deploy/01-ssm.sh
# 2. the policy split, then the CI role (no right is added or removed)
ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
```

Then merge. CI deploys and runs the smokes, which now delete the clients they create (step 3).
After the deploy is green, with the admin profile:

```bash
# 4. prune the test clients, then the factors of every deleted client
ADMIN_PROFILE=default scripts/setup-and-deploy/06-prune-clients.sh
ADMIN_PROFILE=default scripts/setup-and-deploy/07-prune-factors.sh
ADMIN_PROFILE=default scripts/setup-and-deploy/07-prune-factors.sh --apply
```

On stg `06-prune-clients.sh` should propose 52 of 55 clients and keep `auth-app-local`,
`auth-app-stg` and `auth-gate-console-stg`. If the count differs, answer anything but
`delete 52` first (it then deletes nothing) and send the listing. `07-prune-factors.sh` without
`--apply` only prints counts; read them before `--apply`.

5. The proofs, read-only, under the admin profile in a subshell, so it does not leak into later
   deploys. Expect both policies on the user and on the role, the three parameters, the client
   list `auth-app-local`, `auth-app-stg` and `auth-gate-console-stg`, and `401` for the admin
   key on a portal route:

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
aws iam list-attached-user-policies --user-name auth-gate-deployer --query 'AttachedPolicies[].PolicyName'
aws iam list-attached-role-policies --role-name ci-auth-gate-deploy --query 'AttachedPolicies[].PolicyName'
aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=/credo/auth-gate/$STAGE/" \
  --query 'Parameters[].[Name,Type]' --output text
base="$(aws cloudformation describe-stacks --stack-name "auth-gate-$STAGE" \
  --query 'Stacks[0].Outputs[?OutputKey==`BaseUrl`].OutputValue | [0]' --output text)"
k="$(aws ssm get-parameter --name "/credo/auth-gate/$STAGE/admin-key" --with-decryption \
  --query Parameter.Value --output text)"
printf 'x-internal-access-key: %s\n' "$k" | curl -sS -H @- "${base%/}/api/clients" | jq -r '.clients[].name'
printf 'x-internal-access-key: %s\n' "$k" | curl -sS -o /dev/null -w '%{http_code}\n' -H @- \
  -H 'x-tenant-id: 01JD0000000000000000000000' "${base%/}/api/portal/clients"
)
```
