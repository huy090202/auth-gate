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

# Deletion protection fails the stack delete, so it goes off here, after the typed confirmation
# and just before the delete: a failure above leaves the table protected. The table is named
# after the stack (infra/console.yaml). On a rerun it may be gone, or already unprotected.
restore_hint="Table $STACK is left unprotected if it still exists. To restore: aws dynamodb update-table --region $REGION --table-name $STACK --deletion-protection-enabled"
table_open=""
if protected="$(aws dynamodb describe-table --region "$REGION" --table-name "$STACK" \
  --query Table.DeletionProtectionEnabled --output json 2>&1)"; then
  table_open=1
  if [[ "$protected" == true ]]; then
    aws dynamodb update-table --region "$REGION" --table-name "$STACK" --no-deletion-protection-enabled >/dev/null
    # The table-exists waiter only proves ACTIVE, so read the setting itself.
    off=""
    for _ in {1..30}; do
      off="$(aws dynamodb describe-table --region "$REGION" --table-name "$STACK" \
        --query "Table.TableStatus=='ACTIVE' && Table.DeletionProtectionEnabled==\`false\`" --output json)" || off=""
      [[ "$off" == true ]] && break
      sleep 2
    done
    [[ "$off" == true ]] || die "deletion protection on table $STACK did not clear in 60 s; stack not deleted, rerun this script. $restore_hint"
    echo "turned off deletion protection on table $STACK"
  fi
elif [[ "$protected" == *ResourceNotFoundException* ]]; then
  echo "skip deletion protection (table $STACK absent)"
else
  die "cannot read table $STACK: $protected"
fi

if ! { aws cloudformation delete-stack --region "$REGION" --stack-name "$STACK" \
  && aws cloudformation wait stack-delete-complete --region "$REGION" --stack-name "$STACK"; }; then
  [[ -z "$table_open" ]] || echo "$restore_hint" >&2
  die "stack $STACK was not deleted"
fi
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
