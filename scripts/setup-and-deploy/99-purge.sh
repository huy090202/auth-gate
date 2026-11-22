#!/usr/bin/env bash
# Delete stack auth-gate-$STAGE and its three SSM parameters after a typed confirmation.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

echo "This destroys AuthGate's table: every client and every user's factors, with the three parameters."
read -r -p "Type '$STACK' to delete the stack, its table and its SSM parameters: " answer
[ "$answer" = "$STACK" ] || die "confirmation did not match; nothing deleted"

# Deletion protection fails the stack delete, so it goes off here, after the typed confirmation
# and just before the delete. The table is named after the stack (infra/template.yaml). On a
# rerun it may be gone, or already unprotected. Same shape as scripts/console/purge.sh.
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

for name in admin-key origin-verify portal-key; do
  if err="$(aws ssm delete-parameter --region "$REGION" --name "$PARAM_PREFIX/$name" 2>&1)"; then
    echo "deleted $PARAM_PREFIX/$name"
  elif [[ "$err" == *ParameterNotFound* ]]; then
    echo "skip $PARAM_PREFIX/$name (absent)"
  else
    die "delete $PARAM_PREFIX/$name failed: $err"
  fi
done
