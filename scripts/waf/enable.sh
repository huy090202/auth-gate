#!/usr/bin/env bash
# Owner-only: build the WAF web ACL (stack auth-gate-waf-$STAGE, us-east-1) and set the switch.
# The next CI deploy on main reads the switch and attaches the web ACL to the distributions.
#   STAGE=stg ADMIN_PROFILE=default ./enable.sh     (reads "enable <stage>" from stdin)
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

export AWS_PROFILE="${ADMIN_PROFILE:-default}"
cd "$ROOT"
WAF_STACK="auth-gate-waf-$STAGE"
SWITCH="$PARAM_PREFIX/waf-acl-arn"

cat <<TXT
Plan for stage $STAGE:
  1. deploy stack $WAF_STACK (us-east-1) with the web ACL;
  2. write the switch $SWITCH in $REGION.
Cost: a web ACL and its rules bill monthly, plus a per-request fee, from the moment CI attaches it.
Run disable.sh to stop the cost.
TXT
echo "Type 'enable $STAGE' to continue:"
read -r answer || answer=""
if [ "$answer" != "enable $STAGE" ]; then
  echo "not confirmed; nothing changed" >&2
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
aws ssm put-parameter --region "$REGION" --name "$SWITCH" --type String --overwrite \
  --value "$arn" >/dev/null
echo "Switch written: $SWITCH"
echo "Next: GitHub → Actions → ci → Run workflow (branch main). That deploy attaches the web ACL."
