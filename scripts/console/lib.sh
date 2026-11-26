# Shared settings for the Console scripts. Sourced, not run.
# shellcheck shell=bash
STAGE="${STAGE:-stg}"
# With credentials in the environment (CI) leave AWS_PROFILE alone: a named profile that does not exist fails even then.
[ -n "${AWS_ACCESS_KEY_ID:-}" ] || export AWS_PROFILE="${AWS_PROFILE:-auth-gate-deployer}"
REGION="${AWS_REGION:-ap-southeast-1}"
STACK="auth-gate-console-$STAGE"
AUTHGATE_STACK="auth-gate-$STAGE"
PORTAL_STACK="auth-gate-portal-$STAGE"
PARAM_PREFIX="/credo/auth-gate-console/$STAGE"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
export STAGE REGION STACK AUTHGATE_STACK PORTAL_STACK PARAM_PREFIX ROOT

die() { echo "error: $*" >&2; exit 1; }

# output <stack> <key> prints one stack output, or nothing when it is absent.
output() {
  local v
  v="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text)" || return 1
  [[ "$v" == None ]] && v=""
  printf '%s' "$v"
}
