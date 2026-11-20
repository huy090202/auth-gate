# Shared settings for the setup-and-deploy scripts. Sourced, not run.
# shellcheck shell=bash
STAGE="${STAGE:-stg}"
# With credentials in the environment (CI) leave AWS_PROFILE alone: a named profile that does not exist fails even then.
[ -n "${AWS_ACCESS_KEY_ID:-}" ] || export AWS_PROFILE="${AWS_PROFILE:-auth-gate-deployer}"
REGION="${AWS_REGION:-ap-southeast-1}"
STACK="auth-gate-$STAGE"
PARAM_PREFIX="/credo/auth-gate/$STAGE"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
export STAGE REGION STACK PARAM_PREFIX ROOT

die() { echo "error: $*" >&2; exit 1; }
