#!/usr/bin/env bash
# Smoke-test a deployed Portal stage. Reads BaseUrl and RawPortalUrl from the
# stack outputs and checks, in order:
#   1. $BaseUrl/sign-in     200 text/html, the SPA shell, with a CSP that carries
#                           no 'unsafe-inline' anywhere (every CSP header) and
#                           names Turnstile's host in script-src
#   2. $BaseUrl/api/nope    404 {"error":"not_found"...}: /api never falls
#                           through to the shell
#   3. $RawPortalUrl/api/nope  401 {"error":"unauthorized"...}: the Function URL refuses what did not come
#                           through CloudFront. This holds once the second
#                           deploy pass has run; on the first pass the function
#                           fails at init and answers 502.
#   4. $BaseUrl/api/config  200 with a non-empty turnstileSiteKey: the function is up,
#                           which needs the second deploy pass
#   5. Bruno 01-boundary    the refusals that need no credential
# It needs no credentials and prints no secret. Exit is non-zero on any failure.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v curl >/dev/null || die "curl is required"
command -v jq >/dev/null || die "jq is required"

base_url="$(output "$STACK" BaseUrl)"
raw_url="$(output "$STACK" RawPortalUrl)"
[[ -n "$base_url" ]] || die "stack $STACK has no BaseUrl output; run deploy.sh"
[[ -n "$raw_url" ]] || die "stack $STACK has no RawPortalUrl output"
base_url="${base_url%/}"
raw_url="${raw_url%/}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
pass() { echo "ok    $*"; }
fail() { echo "FAIL  $*" >&2; fails=$((fails + 1)); }

# fetch <url> fills $tmp/head and $tmp/body and sets $code. A transport failure
# leaves code 000, which no check below accepts.
fetch() {
  : >"$tmp/body"
  : >"$tmp/head"
  code="$(curl -sS --connect-timeout 10 --max-time 30 -o "$tmp/body" -D "$tmp/head" -w '%{http_code}' "$1")" || code=000
}
# header <name> prints every value of that response header, one per line.
header() {
  tr -d '\r' <"$tmp/head" | awk -F': *' -v n="$1" 'tolower($1) == n { v = substr($0, index($0, ":") + 1); sub(/^ +/, "", v); print v }'
}

# 1. the SPA shell, with a strict CSP
fetch "$base_url/sign-in"
if [[ "$code" == 200 ]]; then pass "GET /sign-in is 200"; else fail "GET /sign-in is $code, want 200"; fi
ctype="$(header content-type)"
if [[ "$ctype" == text/html* ]]; then pass "content-type is $ctype"; else fail "content-type is '$ctype', want text/html"; fi
csp="$(header content-security-policy)"
if [[ -z "$csp" ]]; then
  fail "no content-security-policy header"
else
  # Every CSP header is enforced, so every one is checked. style-src 'self' ships
  # (infra/portal.yaml, portal-web/src/csp.ts): unsafe-inline is a regression.
  if grep -qi 'unsafe-inline' <<<"$csp"; then
    fail "csp carries unsafe-inline"
  else
    pass "csp has no unsafe-inline"
  fi
  if grep -qiE '(^|;) *script-src[^;]* https://challenges\.cloudflare\.com( |;|$)' <<<"$csp"; then
    pass "csp script-src names the Turnstile host"
  else
    fail "csp script-src does not name https://challenges.cloudflare.com"
  fi
fi

# 2. an unknown API path is JSON, not the shell
fetch "$base_url/api/nope"
if [[ "$code" == 404 ]]; then pass "GET /api/nope is 404"; else fail "GET /api/nope is $code, want 404"; fi
if [[ "$(tr -d ' \n\r' <"$tmp/body")" == '{"error":"not_found"'* ]]; then
  pass "/api/nope body is the not_found envelope"
else
  fail "/api/nope body is not {\"error\":\"not_found\"...}"
fi

# 3. the raw Function URL refuses a request that did not come through CloudFront
fetch "$raw_url/api/nope"
if [[ "$code" == 401 ]]; then
  pass "raw function URL is 401"
  if [[ "$(tr -d ' \n\r' <"$tmp/body")" == '{"error":"unauthorized"'* ]]; then
    pass "raw function URL body is the unauthorized envelope"
  else
    fail "raw function URL body is not {\"error\":\"unauthorized\"...}"
  fi
elif [[ "$code" == 502 ]]; then
  fail "raw function URL is 502: the first deploy pass has run but not the second (AUTHGATE_CLIENT_ID=<id> deploy.sh)"
else
  fail "raw function URL is $code, want 401"
fi

# 4. the function answers through CloudFront
fetch "$base_url/api/config"
if [[ "$code" == 200 ]]; then pass "GET /api/config is 200"; else fail "GET /api/config is $code, want 200"; fi
if jq -e '.turnstileSiteKey | length > 0' "$tmp/body" >/dev/null 2>&1; then
  pass "/api/config carries a turnstileSiteKey"
else
  fail "/api/config has no turnstileSiteKey"
fi

# 5. Bruno, the folder that needs no credential. baseUrl is not a secret, so it
# goes on argv. Each request fails on purpose and none needs a credential.
echo "bruno: $base_url"
if (cd "$ROOT/bruno-portal" && ../node_modules/.bin/bru run 01-boundary -r --env-var "baseUrl=$base_url"); then
  pass "bruno 01-boundary"
else
  fail "bruno 01-boundary"
fi

if ((fails > 0)); then
  echo "smoke: $fails check(s) failed" >&2
  exit 1
fi
echo "smoke: all checks passed"
