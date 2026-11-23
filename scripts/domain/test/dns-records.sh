#!/usr/bin/env bash
# Hermetic test for dns-records.sh: a stub `aws` first in PATH answers from the scenario in
# $SCENARIO. Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/dns-records.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub AWS_REGION=ap-southeast-1 CALLS="$tmp/calls"
unset STAGE

# Scenarios: all, no-console, no-output, no-tokens, no-ses, no-cert-param, no-domain, boom.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
args=" $* "
notfound() { echo "An error occurred ($1) when calling the $2 operation: $3" >&2; exit 254; }
case "$args" in
  *" ssm get-parameter "*)
    case "$args" in
      *"--region ap-southeast-1 "*) ;;
      *) echo "stub: ssm outside the app region" >&2; exit 255 ;;
    esac
    case "$args" in
      *"/stg/domain "*)
        [ "$SCENARIO" = no-domain ] && notfound ParameterNotFound GetParameter "x"
        echo example.com ;;
      *"/stg/certificate-arn "*)
        [ "$SCENARIO" = no-cert-param ] && notfound ParameterNotFound GetParameter "x"
        echo arn:cert ;;
      *) echo "stub: unexpected parameter: $args" >&2; exit 255 ;;
    esac ;;
  *" acm describe-certificate "*)
    case "$args" in
      *" --region us-east-1 "*) ;;
      *) echo "stub: acm outside us-east-1" >&2; exit 255 ;;
    esac
    # Apex and wildcard share one record, so ACM lists it twice.
    printf '_x.example.com.\t_y.acm-validations.aws.\n_x.example.com.\t_y.acm-validations.aws.\n' ;;
  *" cloudformation describe-stacks "*)
    case "$args" in
      *"--region ap-southeast-1 "*) ;;
      *) echo "stub: stacks outside the app region" >&2; exit 255 ;;
    esac
    case "$args" in
      *"auth-gate-stg "*) echo d-api.cloudfront.net ;;
      *"auth-gate-console-stg "*)
        [ "$SCENARIO" = no-console ] && notfound ValidationError DescribeStacks "Stack with id auth-gate-console-stg does not exist"
        echo d-admin.cloudfront.net ;;
      *"auth-gate-portal-stg "*)
        [ "$SCENARIO" = boom ] && notfound AccessDenied DescribeStacks "denied"
        echo d-portal.cloudfront.net ;;
      *"auth-app-stg "*)
        [ "$SCENARIO" = no-output ] && echo ""
        [ "$SCENARIO" = no-output ] || echo d-app.cloudfront.net ;;
      *) echo "stub: unexpected stack: $args" >&2; exit 255 ;;
    esac ;;
  *" sesv2 get-email-identity "*)
    case "$SCENARIO" in
      no-ses) notfound NotFoundException GetEmailIdentity "nope" ;;
      no-tokens) echo None ;;
      *) printf 'tk1\ttk2\ttk3\n' ;;
    esac ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
exit 0
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }
check() { if "${@:2}"; then ok "$1"; else nok "$1"; fi; }
# shellcheck disable=SC2329 # called through check
run() {
  export SCENARIO="$1"
  shift
  : >"$CALLS"
  bash "$script" "$@" >"$tmp/out" 2>"$tmp/errout"
}
# shellcheck disable=SC2329 # called through check
# line NAME: the output line whose first field is NAME, squeezed to single spaces.
line() { awk -v n="$1" '$1 == n' "$tmp/out" | tr -s ' '; }
# shellcheck disable=SC2329 # called through check
is() { [ "$(line "$1")" = "$2" ]; }
# shellcheck disable=SC2329 # called through check
count_rows() { [ "$(grep -vc '^#\|^name ' "$tmp/out")" = "$1" ]; }

check "all sources: succeeds" run all
check "all sources: nine records" count_rows 9
check "validation: label relative to the zone, content without trailing dot, once" \
  is _x "_x CNAME _y.acm-validations.aws DNS only"
check "validation printed once" [ "$(grep -c '^_x ' "$tmp/out")" = 1 ]
check "api" is api "api CNAME d-api.cloudfront.net DNS only"
check "admin" is admin "admin CNAME d-admin.cloudfront.net DNS only"
check "portal" is portal "portal CNAME d-portal.cloudfront.net DNS only"
check "apex" is @ "@ CNAME d-app.cloudfront.net DNS only"
check "dkim 1" is tk1._domainkey "tk1._domainkey CNAME tk1.dkim.amazonses.com DNS only"
check "dkim 2" is tk2._domainkey "tk2._domainkey CNAME tk2.dkim.amazonses.com DNS only"
check "dkim 3" is tk3._domainkey "tk3._domainkey CNAME tk3.dkim.amazonses.com DNS only"
check "dmarc" is _dmarc "_dmarc TXT v=DMARC1; p=none; rua=mailto:dmarc@example.com DNS only"
check "header says DNS only" grep -q '^#.*DNS only' "$tmp/out"
check "read only: no write calls" bash -c "! grep -qE 'put-|create-|update-|delete-' '$CALLS'"
check "admin profile passed explicitly" bash -c "! grep -v -- '--profile default' '$CALLS'"

check "console absent: succeeds" run no-console
check "console absent: pending line" is admin "admin CNAME pending (stack auth-gate-console-stg not deployed) DNS only"
check "console absent: others intact" is api "api CNAME d-api.cloudfront.net DNS only"

check "no output: succeeds" run no-output
check "no output: pending line" is @ "@ CNAME pending (stack auth-app-stg has no DistributionDomain output) DNS only"

check "no tokens: succeeds" run no-tokens
check "no tokens: one pending line" is '*._domainkey' "*._domainkey CNAME pending (run ses-identity.sh) DNS only"
check "no tokens: seven records" count_rows 7

check "no SES identity: succeeds" run no-ses
check "no SES identity: pending line" is '*._domainkey' "*._domainkey CNAME pending (run ses-identity.sh) DNS only"

check "no certificate parameter: succeeds" run no-cert-param
check "no certificate parameter: pending" is _acm-validation "_acm-validation CNAME pending (run 00-cert.sh) DNS only"
check "no certificate parameter: ACM not called" bash -c "! grep -q acm '$CALLS'"

# shellcheck disable=SC2329 # called through check
no_domain() { ! run no-domain; }
check "no domain: dies" no_domain
check "no domain: says to run 00-cert.sh" grep -q 'no domain configured; run 00-cert.sh first' "$tmp/errout"

# shellcheck disable=SC2329 # called through check
boom() { ! run boom; }
check "other AWS error: dies" boom

exit "$fail"
