#!/usr/bin/env bash
# Operator commands for the Console's admin accounts:
#   admin.sh create --username U --role master|admin
#   admin.sh reset-password --username U
#   admin.sh disable --username U
#   admin.sh clear-factors --username U
# They write to the Console table with the operator's own AWS profile
# (ADMIN_PROFILE, default "default"), not the deployer's. The password is read
# with read -rs and piped on stdin; it is never on argv. STAGE=local uses
# .env.console and DynamoDB Local instead of AWS.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

sub="${1:-}"
case "$sub" in
  create | reset-password | disable | clear-factors) ;;
  *) echo "usage: $0 create|reset-password|disable|clear-factors --username U [--role master|admin]" >&2; exit 2 ;;
esac
shift

if [[ "$STAGE" == local ]]; then
  unset AWS_PROFILE
  env_file="$ROOT/.env.console"
  [[ -f "$env_file" ]] || die "$env_file is missing; run register-client.sh local first"
  set -a
  # shellcheck disable=SC1090
  . "$env_file"
  set +a
  export CONSOLE_DEV=1 TABLE_NAME=console-dev DYNAMO_ENDPOINT=http://localhost:8000 AUTHGATE_BASE_URL=http://localhost:8080
  export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-x}" AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-x}"
else
  # Nothing left over from a local or test shell may redirect the write.
  unset DYNAMO_ENDPOINT CONSOLE_DEV ADMIN_KEY CLIENT_KEY PORT AUTHGATE_BASE_URL AUTHGATE_CLIENT_ID ADMIN_KEY_PARAM TABLE_NAME
  export AWS_PROFILE="${ADMIN_PROFILE:-default}"
  export AWS_REGION="$REGION"
  export TABLE_NAME="auth-gate-console-$STAGE"
  if [[ "$sub" == clear-factors ]]; then
    AUTHGATE_BASE_URL="$(output "$AUTHGATE_STACK" BaseUrl)"
    AUTHGATE_CLIENT_ID="$(output "$STACK" AuthGateClientId)"
    [[ -n "$AUTHGATE_BASE_URL" ]] || die "stack $AUTHGATE_STACK has no BaseUrl output"
    [[ -n "$AUTHGATE_CLIENT_ID" ]] || die "stack $STACK has no AuthGateClientId output; run register-client.sh stg and deploy again"
    export AUTHGATE_BASE_URL AUTHGATE_CLIENT_ID
    export ADMIN_KEY_PARAM="/credo/auth-gate/$STAGE/admin-key"
  fi
fi

cd "$ROOT"
case "$sub" in
  create | reset-password)
    IFS= read -rsp "Password for the account: " pw
    echo >&2
    IFS= read -rsp "Again: " pw2
    echo >&2
    [[ "$pw" == "$pw2" ]] || die "the two passwords differ"
    printf '%s\n' "$pw" | go run ./cmd/console admin "$sub" "$@"
    ;;
  *)
    go run ./cmd/console admin "$sub" "$@" </dev/null
    ;;
esac
