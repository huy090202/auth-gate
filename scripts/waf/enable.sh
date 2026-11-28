#!/usr/bin/env bash
# Owner-only: build the WAF web ACL (stack auth-gate-waf-$STAGE, us-east-1) and set the switch.
# The next CI deploy on main reads the switch and attaches the web ACL to the distributions.
#   STAGE=stg ADMIN_PROFILE=default ./enable.sh     (reads "enable <stage>" from stdin)
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

cat <<TXT
Plan for stage $STAGE:
  1. deploy stack $WAF_STACK (us-east-1) with the web ACL;
  2. write the switch $SWITCH in $WAF_SWITCH_REGION.
Cost: about 9-10 USD a month while on (5 USD web ACL + 4 rules x 1 USD + 0.60 USD per million requests).
Run disable.sh to stop the cost.
TXT
echo "Type 'enable $STAGE' to continue:"
read -r answer || answer=""
if [ "$answer" != "enable $STAGE" ]; then
  echo "not confirmed; nothing changed" >&2
  exit 1
fi

# A web ACL nothing will attach to is pure cost: the app stack must exist first.
if ! aws cloudformation describe-stacks --stack-name "$STACK" --region "$WAF_SWITCH_REGION" >/dev/null 2>&1; then
  echo "error: app stack $STACK does not exist in $WAF_SWITCH_REGION; deploy it first, nothing changed" >&2
  exit 1
fi

aws cloudformation deploy --template-file infra/waf.yaml --stack-name "$WAF_STACK" \
  --region us-east-1 --parameter-overrides "Stage=$STAGE" --tags app=auth-gate \
  --no-fail-on-empty-changeset
arn="$(aws cloudformation describe-stacks --stack-name "$WAF_STACK" --region us-east-1 \
  --query "Stacks[0].Outputs[?OutputKey=='WebAclArn'].OutputValue" --output text)"
if [ -z "$arn" ] || [ "$arn" = "None" ]; then
  echo "error: stack $WAF_STACK has no WebAclArn output" >&2
  exit 1
fi
# The ARN is not a secret, so a plain --value is fine here.
aws ssm put-parameter --region "$WAF_SWITCH_REGION" --name "$SWITCH" --type String --overwrite \
  --value "$arn" >/dev/null
echo "Switch written: $SWITCH"
echo "Next: GitHub → Actions → ci → Run workflow (branch main). That deploy attaches the web ACL."
