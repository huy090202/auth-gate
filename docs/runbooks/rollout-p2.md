# Rollout of P2 (the Portal)

> Since the operator's side, a new stage also needs `AWS_PROFILE=default scripts/portal/operator-key.sh`
> (create the operator key; it prints `created` or `exists`) after step 4 and before the Portal's
> smoke, because the Portal's Lambda refuses to start without it. The Console deploys after the
> Portal. See [Deploy and CI](../reference/deploy.md#first-deploy-of-a-stage).

This branch adds the Portal and widens the permissions boundary and the deployer policy. It needs a
person at an admin profile, in this order, from the `feat-self-service` worktree, before the merge.
Order matters: until step 3 is done the first deploy cannot succeed, and until step 4 is done CI's
preflight refuses to deploy (the Portal stack and its client id must exist), so the hand deploy
comes first and the merge second. Nothing in the blocks needs editing: each one asks for what it
needs, and no secret goes on a command line.

**1. SES.** The Lambda may send only from `EMAIL_FROM`, so the address typed here must be the one
given to the deploy. In the sandbox SES sends only to verified addresses, so use the address you
will sign up with in step 7. The second command makes SES stop mailing addresses that bounced or
complained.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1
read -r -p 'Sender address: ' addr
aws sesv2 create-email-identity --email-identity "$addr"
aws sesv2 put-account-suppression-attributes --suppressed-reasons BOUNCE COMPLAINT
)
```

SES mails a verification link to that address. Open it before going on; the proofs below show
`true` for `VerifiedForSendingStatus` once it has been clicked.

**2. Turnstile.** In the Cloudflare dashboard, Turnstile, add a widget named `auth-gate-portal-stg`,
mode Managed, with the hostname `localhost` only: the CloudFront host does not exist yet. Copy the
site key (public) and the secret. Step 4 reads the secret from the terminal without echo.

**3. The boundary and the deployer policy**, back to back, in this order. `00-deployer.sh` rewrites
the policies the CI role also holds, and `00-ci-role.sh` re-asserts the role, so run them without
a merge or a deploy in between. Then wait a minute: IAM changes take a moment to apply.

```bash
ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
sleep 60
```

**4. The first Portal deploy**, under the admin profile, two passes with the registration between.
The block stops at the first failing command. On a rerun press Enter at the secret prompt (the
secret is already in SSM and is kept), and skip `register-client.sh` once the client key exists: it
refuses to overwrite it, so run the second pass alone with `AUTHGATE_CLIENT_ID` set.

```bash
cd /home/work/auth-gate/feat-self-service && pnpm install --frozen-lockfile && (
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
read -r -p 'Sender address (the SES identity): ' EMAIL_FROM
read -r -p 'Turnstile site key: ' TURNSTILE_SITE_KEY
read -rs -p 'Turnstile secret (not echoed): ' TURNSTILE_SECRET && echo
export EMAIL_FROM TURNSTILE_SITE_KEY
# The secret goes to pass 1 alone, in its environment, never exported; deploy.sh drops it once
# it is in SSM, before anything is built.
TURNSTILE_SECRET="$TURNSTILE_SECRET" scripts/portal/deploy.sh   # pass 1: /api answers 502 until pass 2
unset TURNSTILE_SECRET
scripts/portal/register-client.sh stg         # prints AUTHGATE_CLIENT_ID=<id>
read -r -p 'AUTHGATE_CLIENT_ID printed above: ' AUTHGATE_CLIENT_ID
export AUTHGATE_CLIENT_ID
scripts/portal/deploy.sh                      # pass 2: sets the client id and PublicBaseUrl
)
```

Now the CloudFront host exists. Print it, add that hostname (without `https://`) to the Turnstile
widget in the dashboard, next to `localhost`, then smoke it:

```bash
AWS_PROFILE=default aws cloudformation describe-stacks --region ap-southeast-1 \
  --stack-name auth-gate-portal-stg \
  --query 'Stacks[0].Outputs[?OutputKey==`BaseUrl`].OutputValue | [0]' --output text
AWS_PROFILE=default scripts/portal/smoke.sh
```

**5. The two repository variables.** In GitHub, the repository, Settings, Secrets and variables,
Actions, the Variables tab, New repository variable: `PORTAL_EMAIL_FROM` is the sender address of
step 1 and `PORTAL_TURNSTILE_SITE_KEY` is the site key of step 2. Without them the merge's Portal
deploy stops at "EMAIL_FROM is required", after AuthGate and the Console have deployed.

**6. Push the branch, open the pull request and merge it.** CI runs `check`, then deploys AuthGate,
the Console and the Portal and runs the three smokes. The preflight passes only because steps 3 and
4 are done. If a smoke fails after a successful deploy, the new code is live: fix forward.

**7. The browser run**, on the Portal's CloudFront URL (step 4 printed it):

1. Sign up with the SES-verified address. With any other address SES refuses to send in the
   sandbox, the Portal logs it and still answers `check_email`, and no mail arrives.
2. Open the verification link; you land in `enroll`.
3. Add a PIN or a passkey; save the ten recovery codes (copy or download) and tick the box.
4. Create a client in the personal workspace; copy its key, then rotate the key and read the new one.
5. Sign out, then sign in with the password and a recovery code instead of the factor: you land in
   `enroll`; add a factor again.
6. Sign out, choose "forgot password", open the link, set a new password and sign in with it.
7. Optional: delete the client and the account from My account.

**The proofs**, read-only, under the admin profile in a subshell, so it does not leak into later
deploys. Expect: the SES identity `true` and the sandbox still on (`ProductionAccessEnabled`
`false`) with suppression `BOUNCE` and `COMPLAINT`; the boundary statements `SendPortalEmail` and
`PortalAvatars`; both managed policies on the user and on the role; the Portal's three parameters
(names and types only: `authgate-client-key` SecureString, `origin-verify` String,
`turnstile-secret` SecureString); the stack complete, with a client id and the sender address;
deletion protection `true`; only a site key in `/api/config`; `401` for the raw URL and `401` for a
tenant route without a cookie. `gh variable list` shows the two variables.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
read -r -p 'Sender address: ' addr
acct="$(aws sts get-caller-identity --query Account --output text)"
aws sesv2 get-email-identity --email-identity "$addr" --query VerifiedForSendingStatus
aws sesv2 get-account --query '[ProductionAccessEnabled,SuppressionAttributes.SuppressedReasons]'
b="arn:aws:iam::$acct:policy/auth-gate-boundary"
v="$(aws iam get-policy --policy-arn "$b" --query Policy.DefaultVersionId --output text)"
aws iam get-policy-version --policy-arn "$b" --version-id "$v" --query 'PolicyVersion.Document.Statement[].Sid'
aws iam list-attached-user-policies --user-name auth-gate-deployer --query 'AttachedPolicies[].PolicyName'
aws iam list-attached-role-policies --role-name ci-auth-gate-deploy --query 'AttachedPolicies[].PolicyName'
aws ssm describe-parameters \
  --parameter-filters "Key=Name,Option=BeginsWith,Values=/credo/auth-gate-portal/$STAGE/" \
  --query 'Parameters[].[Name,Type]' --output text
out() { aws cloudformation describe-stacks --stack-name "auth-gate-portal-$STAGE" \
  --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue | [0]" --output text; }
aws cloudformation describe-stacks --stack-name "auth-gate-portal-$STAGE" --query 'Stacks[0].StackStatus' --output text
echo "AuthGateClientId: $(out AuthGateClientId)"
fn="$(aws cloudformation describe-stack-resource --stack-name "auth-gate-portal-$STAGE" \
  --logical-resource-id Portal --query StackResourceDetail.PhysicalResourceId --output text)"
aws lambda get-function-configuration --function-name "$fn" --query 'Environment.Variables.EMAIL_FROM' --output text
aws dynamodb describe-table --table-name "auth-gate-portal-$STAGE" --query Table.DeletionProtectionEnabled
base="$(out BaseUrl)"
raw="$(out RawPortalUrl)"
curl -sS "${base%/}/api/config" | jq 'keys'
curl -sS -o /dev/null -w '%{http_code}\n' "${raw%/}/api/nope"
curl -sS -o /dev/null -w '%{http_code}\n' "${base%/}/api/t/01JD0000000000000000000000/clients"
)
gh variable list
```
