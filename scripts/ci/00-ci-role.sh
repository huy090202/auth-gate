#!/usr/bin/env bash
# Create the GitHub OIDC identity provider (if the account has none) and the role
# ci-auth-gate-deploy that GitHub Actions assumes to deploy stg from main. The trust policy
# is infra/ci-trust.json; the role carries the managed policy auth-gate-deployer-policy that
# 00-deployer.sh made. The role name does not match auth-gate-*, so the deployer policy cannot
# change it. Rerunnable: an existing provider is used unchanged, an existing role gets its
# trust policy replaced by the file's.
# Run with an admin profile: ADMIN_PROFILE=default ./00-ci-role.sh
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

command -v jq >/dev/null || die "jq is required"

admin=(--profile "${ADMIN_PROFILE:-default}" --region "$REGION")
account_expected=826113468815
role_name=ci-auth-gate-deploy
trust_file="$ROOT/infra/ci-trust.json"

err="$(mktemp)"
trap 'rm -f "$err"' EXIT

account="$(aws sts get-caller-identity "${admin[@]}" --query Account --output text)" ||
  die "cannot read the caller identity"
[ "$account" = "$account_expected" ] ||
  die "account is $account, expected $account_expected (infra/ci-trust.json and the policy name that account)"

policy_arn="arn:aws:iam::$account:policy/auth-gate-deployer-policy"
aws iam get-policy "${admin[@]}" --policy-arn "$policy_arn" >/dev/null 2>"$err" || {
  cat "$err" >&2
  die "cannot find $policy_arn; run 00-deployer.sh first"
}

provider_arn="arn:aws:iam::$account:oidc-provider/token.actions.githubusercontent.com"
if provider="$(aws iam get-open-id-connect-provider "${admin[@]}" \
  --open-id-connect-provider-arn "$provider_arn" --output json 2>"$err")"; then
  if jq -e '.ClientIDList | index("sts.amazonaws.com")' >/dev/null <<<"$provider"; then
    echo "oidc provider exists: $provider_arn"
  else
    die "the oidc provider $provider_arn lacks the client id sts.amazonaws.com; if its owner agrees, run: aws iam add-client-id-to-open-id-connect-provider --open-id-connect-provider-arn $provider_arn --client-id sts.amazonaws.com"
  fi
elif grep -q NoSuchEntity "$err"; then
  # --thumbprint-list is optional: IAM then reads the thumbprint itself, and AWS trusts its
  # own CA store for token.actions.githubusercontent.com anyway.
  aws iam create-open-id-connect-provider "${admin[@]}" \
    --url https://token.actions.githubusercontent.com \
    --client-id-list sts.amazonaws.com >/dev/null
  echo "oidc provider created: $provider_arn"
else
  cat "$err" >&2
  die "cannot read the oidc provider $provider_arn"
fi

if aws iam get-role "${admin[@]}" --role-name "$role_name" >/dev/null 2>"$err"; then
  aws iam update-assume-role-policy "${admin[@]}" --role-name "$role_name" \
    --policy-document "file://$trust_file"
  echo "role exists, trust policy replaced: $role_name"
elif grep -q NoSuchEntity "$err"; then
  aws iam create-role "${admin[@]}" --role-name "$role_name" \
    --assume-role-policy-document "file://$trust_file" \
    --max-session-duration 3600 \
    --description "GitHub Actions deploy of auth-gate stg from main" >/dev/null
  echo "role created: $role_name"
else
  cat "$err" >&2
  die "cannot read the role $role_name"
fi

aws iam attach-role-policy "${admin[@]}" --role-name "$role_name" --policy-arn "$policy_arn"
echo "role arn: arn:aws:iam::$account:role/$role_name"
