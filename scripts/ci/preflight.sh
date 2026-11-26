#!/usr/bin/env bash
# Refuse to deploy from CI unless all three stacks already exist and can be updated. CI never
# creates a stack: a first deploy or a recovery runs by hand under the admin profile.
# Reads STAGE (default stg) and AWS_REGION. Exit 0 only when all three stacks are deployable.
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

err="$(mktemp)"
trap 'rm -f "$err"' EXIT

for stack in "auth-gate-$STAGE" "auth-gate-console-$STAGE" "auth-gate-portal-$STAGE"; do
  status="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$stack" \
    --query 'Stacks[0].StackStatus' --output text 2>"$err")" || {
    grep -q 'does not exist' "$err" || { cat "$err" >&2; die "cannot read $stack"; }
    status=missing
  }
  case "$status" in
    CREATE_COMPLETE | UPDATE_COMPLETE | UPDATE_ROLLBACK_COMPLETE) ;;
    *) die "$stack is $status: a first deploy or a recovery runs by hand under the admin profile (docs/reference/deploy.md, First deploy of a stage)" ;;
  esac
  echo "ok $stack $status"
done

# The backticks are JMESPath literals, not shell. An app's second deploy pass has run only
# when its stack reports the client id it was deployed with.
for app in console portal; do
  # shellcheck disable=SC2016
  client_id="$(aws cloudformation describe-stacks --region "$REGION" \
    --stack-name "auth-gate-$app-$STAGE" \
    --query 'Stacks[0].Outputs[?OutputKey==`AuthGateClientId`].OutputValue | [0]' --output text)"
  case "$client_id" in
    "" | None) die "auth-gate-$app-$STAGE has no AuthGateClientId output: its second deploy pass has not run; do it by hand (docs/reference/deploy.md, First deploy of a stage)" ;;
  esac
done

# The operator key is created by hand (scripts/portal/operator-key.sh), never by CI. Both apps
# read it, so a deploy without it would start Lambdas that refuse to initialise. No
# --with-decryption: only its existence is checked, and the value is never read or printed.
key="/credo/auth-gate-portal/$STAGE/operator-key"
if aws ssm get-parameter --region "$REGION" --name "$key" >/dev/null 2>"$err"; then
  echo "ok $key"
elif grep -q ParameterNotFound "$err"; then
  die "$key is missing: run scripts/portal/operator-key.sh by hand under the admin profile"
else
  cat "$err" >&2
  die "cannot read $key"
fi
