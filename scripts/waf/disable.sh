#!/usr/bin/env bash
# Owner-only, two phases, chosen by state. Run it twice.
#   phase 1 (switch present): delete the switch; CI's next deploy passes WebAclArn=none and detaches.
#   phase 2 (switch absent, stack present): delete the stack, only if no distribution uses the ACL.
#   STAGE=stg ADMIN_PROFILE=default ./disable.sh    (reads "disable <stage>" from stdin)
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

export AWS_PROFILE="${ADMIN_PROFILE:-default}"
cd "$ROOT"
WAF_STACK="auth-gate-waf-$STAGE"
SWITCH="$PARAM_PREFIX/waf-acl-arn"
RUN_LINE="Next: run the ci workflow on main to detach the web ACL, then run disable.sh again to delete the stack."

confirm() {
  echo "Type 'disable $STAGE' to continue:"
  read -r answer || answer=""
  if [ "$answer" != "disable $STAGE" ]; then
    echo "not confirmed; nothing changed" >&2
    exit 1
  fi
}

# Prints the stack's WebAclArn, or nothing when the stack does not exist. A stack that exists
# without the output (DELETE_FAILED, ROLLBACK_COMPLETE) is an error, never "off".
stack_arn() {
  local err out rc=0 status value
  err="$(mktemp)"
  out="$(aws cloudformation describe-stacks --stack-name "$WAF_STACK" --region us-east-1 \
    --query "Stacks[0].[StackStatus, Outputs[?OutputKey=='WebAclArn'].OutputValue | [0]]" \
    --output text 2>"$err")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -q "does not exist" "$err"; then
      rm -f "$err"
      return 0
    fi
    cat "$err" >&2
    rm -f "$err"
    return 1
  fi
  rm -f "$err"
  status="$(printf '%s' "$out" | cut -f1)"
  value="$(printf '%s' "$out" | cut -f2)"
  if [ -z "$value" ] || [ "$value" = "None" ]; then
    echo "error: stack $WAF_STACK exists (status $status) but has no WebAclArn output; fix or delete it by hand" >&2
    return 1
  fi
  printf '%s' "$value"
}

switch="$(waf_acl_arn)" || exit 1
if [ -n "$switch" ]; then
  confirm
  aws ssm delete-parameter --region "$REGION" --name "$SWITCH"
  echo "Switch deleted: $SWITCH"
  echo "$RUN_LINE"
  exit 0
fi

stack="$(stack_arn)" || exit 1
if [ -z "$stack" ]; then
  echo "already off"
  exit 0
fi

confirm
used="$(aws cloudfront list-distributions-by-web-acl-id --region us-east-1 --web-acl-id "$stack" \
  --query 'DistributionList.Items[].Id' --output text)"
[ "$used" = "None" ] && used=""
if [ -n "$used" ]; then
  echo "The web ACL is still attached to: $used"
  echo "$RUN_LINE"
  exit 1
fi
aws cloudformation delete-stack --stack-name "$WAF_STACK" --region us-east-1
aws cloudformation wait stack-delete-complete --stack-name "$WAF_STACK" --region us-east-1
echo "WAF off: no web ACL, no WAF cost."
