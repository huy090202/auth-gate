#!/usr/bin/env bash
# Deploy stack auth-gate-$STAGE and print its one BaseUrl.
#
# The custom domain comes from SSM (scripts/domain/lib.sh): with /credo/auth-gate/$STAGE/domain
# set, AuthGate serves on api.<domain> with the certificate-arn parameter, and the deploy stops
# before sam unless that certificate is ISSUED. DNS is never written here.
#
# Without a custom domain, BASE_URL on verify must be the distribution's own
# domain, but the distribution depends on the Function URLs, which depend on the
# functions: referencing it would be a cycle. So the stack takes it as the
# DistributionBaseUrl parameter, read from the previous deploy's output. On the
# very first deploy it is unknown, so this script deploys twice.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# shellcheck source=../domain/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../domain/lib.sh"

# shellcheck source=../waf/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../waf/lib.sh"

domain_config
web_acl_arn="$(waf_acl_arn)" || exit 1
[ -f "$ROOT/bin/bootstrap.zip" ] || die "bin/bootstrap.zip missing; run 02-build.sh"

# Prints one stack output. A stack that does not exist yet (first deploy) yields
# an empty value; any other AWS error is fatal and shown.
output() {
  local out
  if ! out="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text 2>&1)"; then
    case "$out" in
      *"does not exist"*) return 0 ;;
      *) die "describe-stacks failed: $out" ;;
    esac
  fi
  printf '%s\n' "$out"
}

deploy() {
  # sam rejects empty overrides ("DomainName="), so pass only the set ones;
  # the template defaults every optional parameter to "".
  local params=("Stage=$STAGE")
  [ -z "$DOMAIN" ] || params+=("DomainName=$(app_host api)" "CertificateArn=$CERT_ARN")
  [ -z "$1" ] || params+=("DistributionBaseUrl=$1")
  # Read once per run, passed on both passes. Off is the sentinel none: sam rejects empty overrides and an omitted one keeps the previous value, so neither could detach the web ACL.
  params+=("WebAclArn=${web_acl_arn:-none}")
  # The SAM bucket is shared by every project in the account; the deployer may write only
  # under auth-gate-* in it (infra/deployer-policy.json).
  sam deploy --template-file "$ROOT/infra/template.yaml" \
    --stack-name "$STACK" --region "$REGION" \
    --capabilities CAPABILITY_IAM --resolve-s3 --s3-prefix "$STACK" \
    --no-confirm-changeset --no-fail-on-empty-changeset \
    --parameter-overrides "${params[@]}"
}

dist_url=""
if [ -z "$DOMAIN" ]; then
  d="$(output DistributionDomain)"
  [ -z "$d" ] || [ "$d" = "None" ] || dist_url="https://$d"
fi

deploy "$dist_url"

if [ -z "$DOMAIN" ]; then
  d="$(output DistributionDomain)"
  if [ "https://$d" != "$dist_url" ]; then
    echo "second pass: setting BASE_URL to https://$d"
    deploy "https://$d"
  fi
fi

echo "BaseUrl: $(output BaseUrl)"
echo "RawVerifyUrl: $(output RawVerifyUrl)"
