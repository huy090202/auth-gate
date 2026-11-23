#!/usr/bin/env bash
# Hermetic test for 00-cert.sh: a stub `aws` first in PATH records every call and answers from
# the scenario in $SCENARIO. Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

cert="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../setup-and-deploy" && pwd)/00-cert.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1 POLL_SECONDS=0 MAX_POLLS=5
export CALLS="$tmp/calls" POLLS="$tmp/polls" PUTS="$tmp/puts"

# Scenarios: none, issued, failed-only, wait-issued (pending, pending, issued), wait-failed.
# The stub's list-certificates is already filtered by status, as the real one is: a `failed-only`
# account lists nothing. It prints "arn<TAB>status" rows, as the script's --query asks.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
args=" $* "
case "$args" in
  *" acm "*)
    case "$args" in
      *" --region us-east-1 "*) ;;
      *) echo "stub: acm call without --region us-east-1: $args" >&2; exit 255 ;;
    esac ;;
esac
case "$args" in
  *" acm list-certificates "*)
    case "$SCENARIO" in
      issued) printf 'arn:old-pending\tPENDING_VALIDATION\narn:issued\tISSUED\n' ;;
      wait-issued | wait-failed) printf 'arn:new\tPENDING_VALIDATION\n' ;;
    esac ;;
  *" acm request-certificate "*) echo arn:new ;;
  *" acm describe-certificate "*)
    case "$args" in
      *ResourceRecord*) printf '_x.example.com.\t_y.acm-validations.aws.\n' ;;
      *Certificate.Status*)
        echo x >>"$POLLS"
        n="$(wc -l <"$POLLS")"
        case "$SCENARIO" in
          wait-failed) [ "$n" -ge 2 ] && echo FAILED || echo PENDING_VALIDATION ;;
          *) [ "$n" -ge 3 ] && echo ISSUED || echo PENDING_VALIDATION ;;
        esac ;;
      *) echo "stub: unexpected describe: $args" >&2; exit 255 ;;
    esac ;;
  *" ssm put-parameter "*)
    f="${args##*--cli-input-json file://}"
    f="${f%% *}"
    cat "$f" >>"$PUTS" ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
exit 0
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }
assert() {
  local msg="$1"
  shift
  if "$@" >/dev/null 2>&1; then ok "$msg"; else nok "$msg"; fi
}
refute() {
  local msg="$1"
  shift
  if "$@" >/dev/null 2>&1; then nok "$msg"; else ok "$msg"; fi
}
calls() { grep -cF -- "$1" "$CALLS" || true; }
# shellcheck disable=SC2329 # called through assert and refute
run() {
  export SCENARIO="$1"
  shift
  : >"$CALLS"
  : >"$POLLS"
  : >"$PUTS"
  bash "$cert" "$@" >"$tmp/out" 2>&1
}
has_call() { grep -qF -- "$1" "$CALLS"; }
has_put() { grep -q -- "$1" "$PUTS"; }

# No certificate: one request with the SAN and the tag; both parameters written.
assert "no certificate: script succeeds" run none example.com
if [ "$(calls 'acm request-certificate')" = 1 ]; then ok "no certificate: one request"; else nok "no certificate: one request"; fi
if has_call "--subject-alternative-names *.example.com"; then ok "request carries the wildcard SAN"; else nok "request carries the wildcard SAN"; fi
if has_call "--domain-name example.com" && has_call "--validation-method DNS"; then ok "request: domain, DNS validation"; else nok "request: domain, DNS validation"; fi
if has_call "Key=app,Value=auth-gate"; then ok "request carries the app tag"; else nok "request carries the app tag"; fi
if has_call "--region us-east-1 acm request-certificate"; then ok "request in us-east-1"; else nok "request in us-east-1"; fi
if [ "$(calls 'ssm put-parameter')" = 2 ]; then ok "two put-parameter calls"; else nok "two put-parameter calls"; fi
if has_call "--profile default"; then ok "admin profile passed explicitly"; else nok "admin profile passed explicitly"; fi
if has_call "--region ap-southeast-1 ssm put-parameter"; then ok "parameters written in ap-southeast-1"; else nok "parameters written in ap-southeast-1"; fi
if has_put '"Name":"/credo/auth-gate/t/domain","Value":"example.com"'; then ok "domain parameter"; else nok "domain parameter"; fi
if has_put '"Name":"/credo/auth-gate/t/certificate-arn","Value":"arn:new"'; then ok "certificate-arn parameter"; else nok "certificate-arn parameter"; fi
if has_put '"Type":"String","Overwrite":true'; then ok "String, overwrite"; else nok "String, overwrite"; fi
if grep -q '_x.example.com.' "$tmp/out" && grep -q '_y.acm-validations.aws.' "$tmp/out"; then ok "validation record printed"; else nok "validation record printed"; fi

# An ISSUED certificate is listed (beside a pending one): reused, no request.
assert "issued listed: script succeeds" run issued example.com
if [ "$(calls 'acm request-certificate')" = 0 ]; then ok "issued listed: no request"; else nok "issued listed: no request"; fi
if has_put '"Value":"arn:issued"'; then ok "issued listed: the issued ARN is written"; else nok "issued listed: the issued ARN is written"; fi
if has_call "--certificate-statuses ISSUED PENDING_VALIDATION"; then ok "list asks only ISSUED and PENDING_VALIDATION"; else nok "list asks only ISSUED and PENDING_VALIDATION"; fi

# Only a FAILED one exists: the list filters it out, so a new certificate is requested.
assert "failed only: script succeeds" run failed-only example.com
if [ "$(calls 'acm request-certificate')" = 1 ]; then ok "failed only: a request"; else nok "failed only: a request"; fi

# --wait: pending, pending, issued.
assert "--wait: exits 0 once issued" run wait-issued example.com --wait
if [ "$(wc -l <"$POLLS")" = 3 ]; then ok "--wait: three polls"; else nok "--wait: three polls"; fi
if [ "$(calls 'acm request-certificate')" = 0 ]; then ok "--wait: pending certificate reused"; else nok "--wait: pending certificate reused"; fi

# A listing asks for a certificate whose SANs include the wildcard (the stub cannot evaluate the
# query, so the test pins its text).
if has_call "contains(SubjectAlternativeNameSummaries || \`[]\`, '*.example.com')"; then ok "list matches only a certificate with the wildcard SAN"; else nok "list matches only a certificate with the wildcard SAN"; fi

# A mixed-case argument is lowercased everywhere: the request, the query and the parameter.
assert "mixed case: script succeeds" run none Example.COM
if has_call "--domain-name example.com" && has_call "*.example.com"; then ok "mixed case: request uses the lowercase domain"; else nok "mixed case: request uses the lowercase domain"; fi
if has_put '"Name":"/credo/auth-gate/t/domain","Value":"example.com"'; then ok "mixed case: SSM domain is lowercase"; else nok "mixed case: SSM domain is lowercase"; fi
refute "mixed case: no uppercase reaches aws" grep -q 'Example\|COM' "$CALLS" "$PUTS"

refute "--wait: FAILED exits non-zero" run wait-failed example.com --wait
refute "no domain: usage error" run none
refute "unknown flag: usage error" run none example.com --nope

exit "$fail"
