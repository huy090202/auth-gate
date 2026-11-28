#!/usr/bin/env bash
# Hermetic test for scripts/setup-and-deploy/03-deploy.sh and the domain parameters it passes:
# stub `aws` and `sam` first in PATH, the scripts copied into a scratch tree. Nothing reaches
# AWS, and ~/.aws is never read.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home" "$tmp/tree/scripts/setup-and-deploy" "$tmp/tree/scripts/domain" "$tmp/tree/scripts/waf" \
  "$tmp/tree/infra" "$tmp/tree/bin"
cp "$root/scripts/setup-and-deploy/lib.sh" "$root/scripts/setup-and-deploy/03-deploy.sh" "$tmp/tree/scripts/setup-and-deploy/"
cp "$root/scripts/domain/lib.sh" "$tmp/tree/scripts/domain/"
cp "$root/scripts/waf/lib.sh" "$tmp/tree/scripts/waf/"
: >"$tmp/tree/bin/bootstrap.zip"
: >"$tmp/tree/infra/template.yaml"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1

# Scenarios: absent (no domain), issued, pending.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
args=" $* "
notfound() { echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254; }
case "$args" in
  *" ssm get-parameter "*)
    case "$args" in
      *"/credo/auth-gate/t/domain "*) [ "$SCENARIO" = absent ] && notfound; echo authgatehq.com ;;
      *"/credo/auth-gate/t/waf-acl-arn "*)
        case "${WAF:-off}" in
          off) notfound ;;
          on) echo arn:aws:wafv2:us-east-1:111122223333:global/webacl/auth-gate-t/abc ;;
          *) echo "An error occurred (AccessDeniedException) when calling the GetParameter operation" >&2; exit 254 ;;
        esac ;;
      *"/credo/auth-gate/t/certificate-arn "*) echo arn:aws:acm:us-east-1:111122223333:certificate/abc ;;
      *) echo "stub: unexpected parameter: $args" >&2; exit 255 ;;
    esac ;;
  *" acm describe-certificate "*)
    [ "$SCENARIO" = pending ] && echo PENDING_VALIDATION || echo ISSUED ;;
  *" cloudformation describe-stacks "*)
    case "$args" in
      *DistributionDomain*)
        # TWOPASS: the first read finds no stack yet (empty), so the script deploys twice.
        if [ -n "${TWOPASS:-}" ] && [ ! -e "$TWOPASS" ]; then : >"$TWOPASS"; echo None; else echo d111.cloudfront.net; fi ;;
      *) echo "https://out.example" ;;
    esac ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
STUB
cat >"$tmp/bin/sam" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$SAM_LOG"
STUB
chmod +x "$tmp/bin/aws" "$tmp/bin/sam"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }

run() {
  export WAF="${WAF:-off}"
  export SCENARIO="$1" SAM_LOG="$tmp/sam.log"
  : >"$SAM_LOG"
  rc=0
  bash "$tmp/tree/scripts/setup-and-deploy/03-deploy.sh" >"$tmp/out" 2>&1 || rc=$?
}

run absent
if [ "$rc" -eq 0 ] && ! grep -q "DomainName=" "$tmp/sam.log" && ! grep -q "CertificateArn=" "$tmp/sam.log" \
  && grep -q "DistributionBaseUrl=https://d111.cloudfront.net" "$tmp/sam.log"; then
  ok "no domain: no DomainName or CertificateArn override, the CloudFront base url is passed"
else nok "no domain: overrides wrong (rc=$rc)"; cat "$tmp/sam.log" "$tmp/out"; fi

run issued
if [ "$rc" -eq 0 ] && [ "$(wc -l <"$SAM_LOG")" -eq 1 ] \
  && grep -q "DomainName=api.authgatehq.com" "$SAM_LOG" \
  && grep -q "CertificateArn=arn:aws:acm:us-east-1:111122223333:certificate/abc" "$SAM_LOG" \
  && ! grep -q -e "DistributionBaseUrl" -e "HostedZoneId" "$SAM_LOG"; then
  ok "domain: one sam deploy with DomainName=api.<domain> and the certificate"
else nok "domain: overrides wrong (rc=$rc)"; cat "$SAM_LOG" "$tmp/out"; fi

run pending
if [ "$rc" -ne 0 ] && [ ! -s "$SAM_LOG" ] && grep -q "00-cert.sh" "$tmp/out"; then
  ok "domain with a pending certificate: dies before sam deploy, naming 00-cert.sh"
else nok "pending certificate did not stop the deploy (rc=$rc)"; cat "$SAM_LOG" "$tmp/out"; fi

# Why: a read error must never quietly detach a WAF that is on.
export TWOPASS="$tmp/twopass"
rm -f "$TWOPASS"
WAF=off run absent
if [ "$rc" -eq 0 ] && [ "$(wc -l <"$SAM_LOG")" -eq 2 ] && [ "$(grep -c -e ' WebAclArn=\(\s\|$\)' "$SAM_LOG")" -eq 2 ]; then
  ok "WAF off: both passes pass an empty WebAclArn"
else nok "WAF off: WebAclArn= missing (rc=$rc)"; cat "$SAM_LOG" "$tmp/out"; fi

rm -f "$TWOPASS"
WAF=on run absent
if [ "$rc" -eq 0 ] && [ "$(grep -c -e 'WebAclArn=arn:aws:wafv2:us-east-1:111122223333:global/webacl/auth-gate-t/abc' "$SAM_LOG")" -eq 2 ]; then
  ok "WAF on: both passes pass the web ACL arn"
else nok "WAF on: WebAclArn missing (rc=$rc)"; cat "$SAM_LOG" "$tmp/out"; fi

rm -f "$TWOPASS"
WAF=broken run absent
if [ "$rc" -ne 0 ] && [ ! -s "$SAM_LOG" ]; then
  ok "WAF switch unreadable: dies before sam deploy"
else nok "WAF switch unreadable did not stop the deploy (rc=$rc)"; cat "$SAM_LOG" "$tmp/out"; fi

exit "$fail"
