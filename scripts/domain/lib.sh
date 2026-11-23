# Reader for the custom-domain configuration. Sourced by the deploy scripts, not run.
# shellcheck shell=bash
#
# The caller has already sourced scripts/setup-and-deploy/lib.sh, which provides STAGE, REGION
# and die(). This file uses the ambient AWS_PROFILE or credentials (the deployer, in CI too),
# never ADMIN_PROFILE, and defines only the two functions below.
#
#   domain_config   sets DOMAIN and CERT_ARN from SSM. With no `domain` parameter both are
#                   empty (deploy on the CloudFront address). With one, dies unless the
#                   certificate-arn parameter exists and that ACM certificate is ISSUED.
#   app_host PREFIX echoes PREFIX.DOMAIN, or DOMAIN for an empty prefix.

# _domain_param NAME: prints the value of /credo/auth-gate/$STAGE/NAME, or nothing when the
# parameter does not exist. Any other failure dies.
_domain_param() {
  local out err rc=0
  err="$(mktemp)"
  out="$(aws ssm get-parameter --region "$REGION" --name "/credo/auth-gate/$STAGE/$1" \
    --query Parameter.Value --output text 2>"$err")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -q ParameterNotFound "$err"; then
      rm -f "$err"
      return 0
    fi
    echo "error: reading /credo/auth-gate/$STAGE/$1 failed:" >&2
    cat "$err" >&2
    rm -f "$err"
    exit 1
  fi
  rm -f "$err"
  printf '%s' "$out"
}

domain_config() {
  DOMAIN="$(_domain_param domain)"
  CERT_ARN=""
  if [ -z "$DOMAIN" ]; then
    return 0
  fi
  CERT_ARN="$(_domain_param certificate-arn)"
  [ -n "$CERT_ARN" ] || die "domain $DOMAIN is configured but /credo/auth-gate/$STAGE/certificate-arn is not: run scripts/setup-and-deploy/00-cert.sh $DOMAIN"
  local status
  status="$(aws acm describe-certificate --region us-east-1 --certificate-arn "$CERT_ARN" \
    --query Certificate.Status --output text)" || die "cannot describe certificate $CERT_ARN"
  [ "$status" = ISSUED ] || die "certificate $CERT_ARN is $status, not ISSUED: add its validation CNAME and run scripts/setup-and-deploy/00-cert.sh $DOMAIN --wait"
  export DOMAIN CERT_ARN
}

app_host() {
  if [ -n "${1:-}" ]; then echo "$1.$DOMAIN"; else echo "$DOMAIN"; fi
}
