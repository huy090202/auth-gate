#!/usr/bin/env bash
# Refuse to deploy from CI unless both stacks already exist and can be updated. CI never
# creates a stack: a first deploy or a recovery runs by hand under the admin profile.
# Reads STAGE (default stg) and AWS_REGION. Exit 0 only when both stacks are deployable.
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

err="$(mktemp)"
trap 'rm -f "$err"' EXIT

for stack in "auth-gate-$STAGE" "auth-gate-console-$STAGE"; do
  status="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$stack" \
    --query 'Stacks[0].StackStatus' --output text 2>"$err")" || {
    grep -q 'does not exist' "$err" || { cat "$err" >&2; die "cannot read $stack"; }
    status=missing
  }
  case "$status" in
    CREATE_COMPLETE | UPDATE_COMPLETE | UPDATE_ROLLBACK_COMPLETE) ;;
    *) die "$stack is $status: a first deploy or a recovery runs by hand under the admin profile (README, First deploy of a stage)" ;;
  esac
  echo "ok $stack $status"
done

# The backticks are JMESPath literals, not shell.
# shellcheck disable=SC2016
client_id="$(aws cloudformation describe-stacks --region "$REGION" \
  --stack-name "auth-gate-console-$STAGE" \
  --query 'Stacks[0].Outputs[?OutputKey==`AuthGateClientId`].OutputValue | [0]' --output text)"
case "$client_id" in
  "" | None) die "auth-gate-console-$STAGE has no AuthGateClientId output: its second deploy pass has not run; do it by hand (README, First deploy of a stage)" ;;
esac
