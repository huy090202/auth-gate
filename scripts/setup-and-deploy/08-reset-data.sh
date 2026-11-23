#!/usr/bin/env bash
# Reset stg's user data: delete everything in the four DynamoDB tables except the three app
# clients (Console, Portal, auth-app) and the Console admins, and empty the Portal's avatar
# bucket under avatars/ and pending/. Without --apply it only prints what it would delete.
# With --apply it prints that report first, then asks for the typed confirmation.
#
# This needs dynamodb:Scan and s3:DeleteObject, which the deployer lacks, so it runs under the
# operator's profile:
#   ADMIN_PROFILE=default ./08-reset-data.sh [--apply]
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

apply=0
case "${1:-}" in
  "") ;;
  --apply) apply=1 ;;
  *) die "usage: 08-reset-data.sh [--apply]" ;;
esac
[ "$#" -le 1 ] || die "usage: 08-reset-data.sh [--apply]"
[ "$STAGE" = stg ] || die "this resets stg only (STAGE=$STAGE)"

ADMIN_PROFILE="${ADMIN_PROFILE:-default}"
awsa() { aws --profile "$ADMIN_PROFILE" --region "$REGION" "$@"; }

# read_value <name> <stack> <query>: the value must be a single plain token, not empty or None.
read_value() {
  local v
  v="$(awsa cloudformation describe-stacks --stack-name "$2" --query "$3" --output text)" \
    || die "cannot read $1 of $2"
  [[ "$v" != None && "$v" =~ ^[A-Za-z0-9_.-]+$ ]] || die "$2 has no usable $1 (got '$v'); nothing deleted"
  value="$v"
}
output_query() { echo "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue"; }

read_value AuthGateClientId "auth-gate-console-$STAGE" "$(output_query AuthGateClientId)"
console_id="$value"
read_value AuthGateClientId "auth-gate-portal-$STAGE" "$(output_query AuthGateClientId)"
portal_id="$value"
read_value AuthGateClientId "auth-app-$STAGE" "Stacks[0].Parameters[?ParameterKey=='AuthGateClientId'].ParameterValue"
app_id="$value"
read_value AvatarBucket "auth-gate-portal-$STAGE" "$(output_query AvatarBucket)"
bucket="$value"

cd "$ROOT"
reset_tables() { # reset_tables [-apply]
  AWS_PROFILE="$ADMIN_PROFILE" AWS_REGION="$REGION" go run ./cmd/stg-reset -stage "$STAGE" \
    -console "$console_id" -portal "$portal_id" -authapp "$app_id" "$@"
}
reset_avatars() { # reset_avatars [--dryrun]
  local prefix
  for prefix in avatars pending; do
    awsa s3 rm "s3://$bucket/$prefix/" --recursive "$@"
  done
}

reset_tables
reset_avatars --dryrun
[ "$apply" -eq 1 ] || exit 0

printf "Type 'reset stg' to delete the items above: " >&2
read -r answer || answer=""
[ -t 0 ] || echo >&2 # piped input leaves the prompt line open
[ "$answer" = "reset stg" ] || die "confirmation did not match; nothing deleted"

reset_tables -apply
reset_avatars
echo "done; run the four smokes"
