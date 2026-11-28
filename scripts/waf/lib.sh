# Reader for the WAF switch. Sourced by the app deploy scripts, not run.
# shellcheck shell=bash
#
# The caller has already sourced its own lib.sh, which provides STAGE and REGION. This file uses
# the ambient AWS_PROFILE or credentials (the deployer, in CI too), never ADMIN_PROFILE.
#
#   waf_acl_arn   prints the value of /credo/auth-gate/$STAGE/waf-acl-arn, or nothing when the
#                 parameter does not exist (the WAF is off). Any other failure prints the error
#                 on stderr and returns non-zero. Call it as `arn="$(waf_acl_arn)" || exit 1`.

# The switch lives in the app region, whatever AWS_REGION the caller exported: CI reads it there.
WAF_SWITCH_REGION=ap-southeast-1

waf_acl_arn() {
  local name="/credo/auth-gate/$STAGE/waf-acl-arn" out err rc=0
  err="$(mktemp)"
  out="$(aws ssm get-parameter --region "$WAF_SWITCH_REGION" --name "$name" \
    --query Parameter.Value --output text 2>"$err")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -q ParameterNotFound "$err"; then
      rm -f "$err"
      return 0
    fi
    echo "error: reading $name failed:" >&2
    cat "$err" >&2
    rm -f "$err"
    return 1
  fi
  rm -f "$err"
  printf '%s' "$out"
}
