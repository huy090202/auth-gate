#!/usr/bin/env bash
# shellcheck disable=SC2016 # the probe bodies expand inside the child shell
# Hermetic test for scripts/domain/lib.sh: a stub `aws` first in PATH answers from $SCENARIO.
# Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1

# Scenarios: absent, no-arn, pending, issued, denied.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
args=" $* "
notfound() { echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254; }
case "$args" in
  *" ssm get-parameter "*)
    [ "$SCENARIO" = denied ] && { echo "An error occurred (AccessDeniedException) when calling the GetParameter operation" >&2; exit 254; }
    case "$args" in
      *"/credo/auth-gate/t/domain "*) [ "$SCENARIO" = absent ] && notfound; echo example.com ;;
      *"/credo/auth-gate/t/certificate-arn "*) [ "$SCENARIO" = no-arn ] && notfound; echo arn:cert ;;
      *) echo "stub: unexpected parameter: $args" >&2; exit 255 ;;
    esac ;;
  *" --region us-east-1 "*" acm describe-certificate "* | *" acm describe-certificate "*--region\ us-east-1*)
    [ "$SCENARIO" = pending ] && echo PENDING_VALIDATION || echo ISSUED ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }

# Each case runs in a fresh shell, as a deploy script would source the libs.
probe() {
  export SCENARIO="$1"
  bash -c '
    set -euo pipefail
    . "$1/scripts/setup-and-deploy/lib.sh"
    . "$1/scripts/domain/lib.sh"
    '"$2"'
  ' _ "$root" >"$tmp/out" 2>&1
}

if probe absent 'domain_config; echo "[$DOMAIN][$CERT_ARN]"' && [ "$(cat "$tmp/out")" = "[][]" ]; then ok "domain absent: both empty, exit 0"; else nok "domain absent: both empty, exit 0"; fi

if probe no-arn 'domain_config'; then nok "domain without arn: dies"; else
  if grep -q 00-cert.sh "$tmp/out"; then ok "domain without arn: dies naming 00-cert.sh"; else nok "domain without arn: dies naming 00-cert.sh"; fi
fi

if probe pending 'domain_config'; then nok "pending certificate: dies"; else
  if grep -q 00-cert.sh "$tmp/out"; then ok "pending certificate: dies naming 00-cert.sh"; else nok "pending certificate: dies naming 00-cert.sh"; fi
fi

if probe issued 'domain_config; echo "[$DOMAIN][$CERT_ARN]"' && [ "$(cat "$tmp/out")" = "[example.com][arn:cert]" ]; then ok "issued: both set"; else nok "issued: both set"; fi

if probe denied 'domain_config'; then nok "SSM AccessDenied: dies"; else
  if grep -q AccessDenied "$tmp/out"; then ok "SSM AccessDenied: dies, error shown"; else nok "SSM AccessDenied: dies, error shown"; fi
fi

if probe issued 'DOMAIN=authgatehq.com; echo "$(app_host api) $(app_host "")"' && [ "$(cat "$tmp/out")" = "api.authgatehq.com authgatehq.com" ]; then ok "app_host"; else nok "app_host"; fi

exit "$fail"
