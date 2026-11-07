#!/usr/bin/env bash
# Empty the site bucket, delete stack auth-gate-console-$STAGE and its two own SSM
# parameters (origin-verify, authgate-client-key), after a typed confirmation.
# It never touches /credo/auth-gate/$STAGE/admin-key: that one belongs to auth-gate.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

echo "This destroys the Console's table: every admin account and the audit trail, with the site and two parameters."
read -r -p "Type '$STACK' to delete the stack, its table, its bucket contents and SSM parameters: " answer
[[ "$answer" == "$STACK" ]] || die "confirmation did not match; nothing deleted"

# Read before the stack goes: the hint at the end needs the real client id.
client_id="$(output "$STACK" AuthGateClientId 2>/dev/null || true)"
authgate_url="$(output "$AUTHGATE_STACK" BaseUrl 2>/dev/null || true)"

# A non-empty bucket blocks stack deletion. A missing stack means nothing to empty.
if bucket="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='SiteBucket'].OutputValue" --output text 2>/dev/null)" \
  && [[ -n "$bucket" && "$bucket" != None ]]; then
  aws s3 rm "s3://$bucket" --recursive --region "$REGION"
  echo "emptied $bucket"
fi

aws cloudformation delete-stack --region "$REGION" --stack-name "$STACK"
aws cloudformation wait stack-delete-complete --region "$REGION" --stack-name "$STACK"
echo "deleted stack $STACK"

for name in origin-verify authgate-client-key; do
  if err="$(aws ssm delete-parameter --region "$REGION" --name "$PARAM_PREFIX/$name" 2>&1)"; then
    echo "deleted $PARAM_PREFIX/$name"
  elif [[ "$err" == *ParameterNotFound* ]]; then
    echo "skip $PARAM_PREFIX/$name (absent)"
  else
    die "delete $PARAM_PREFIX/$name failed: $err"
  fi
done

cat <<MSG
The Console's client is still registered on auth-gate. Hard delete is out of v1:
disable it instead, so its key stops working. With the admin key in the
x-internal-access-key header:
  POST ${authgate_url:-<auth-gate BaseUrl>}/api/clients/${client_id:-<clientId>}/disable
MSG
