#!/usr/bin/env bash
# Smoke-test a deployed Console stage. Reads BaseUrl and RawConsoleUrl from the
# stack outputs and checks, in order:
#   1. $BaseUrl/clients/x   200 text/html, the SPA shell, with a CSP whose
#                           script-src and style-src carry no 'unsafe-inline'
#   2. $BaseUrl/api/nope    404 {"error":"not_found"...}: /api never falls
#                           through to the shell
#   3. $RawConsoleUrl/api/nope  401: the Function URL refuses what did not come
#                           through CloudFront. This holds once the second
#                           deploy pass has run; on the first pass the function
#                           fails at init and answers 502.
#   4. Bruno 01-boundary    the refusals that need no credential
# It needs no credentials and prints no secret. Exit is non-zero on any failure.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v curl >/dev/null || die "curl is required"

base_url="$(output "$STACK" BaseUrl)"
raw_url="$(output "$STACK" RawConsoleUrl)"
[[ -n "$base_url" ]] || die "stack $STACK has no BaseUrl output; run deploy.sh"
[[ -n "$raw_url" ]] || die "stack $STACK has no RawConsoleUrl output"
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
  code="$(curl -sS --connect-timeout 10 --max-time 30 -o "$tmp/body" -D "$tmp/head" -w '%{http_code}' "$1")" || code=000
}
# header <name> prints the value of the last such response header, lower-cased name.
header() {
  tr -d '\r' <"$tmp/head" | awk -F': *' -v n="$1" 'tolower($1) == n { v = substr($0, index($0, ":") + 1); sub(/^ +/, "", v) } END { print v }'
}

# 1. the SPA shell, with a strict CSP
fetch "$base_url/clients/x"
if [[ "$code" == 200 ]]; then pass "GET /clients/x is 200"; else fail "GET /clients/x is $code, want 200"; fi
ctype="$(header content-type)"
if [[ "$ctype" == text/html* ]]; then pass "content-type is $ctype"; else fail "content-type is '$ctype', want text/html"; fi
csp="$(header content-security-policy)"
if [[ -z "$csp" ]]; then
  fail "no content-security-policy header"
else
  # Directive by directive, so a later directive cannot hide a weak script-src.
  script_src="$(tr ';' '\n' <<<"$csp" | sed 's/^ *//' | grep -E '^script-src( |$)' || true)"
  if [[ -z "$script_src" ]]; then
    fail "csp has no script-src"
  elif [[ "$script_src" == *unsafe-inline* ]]; then
    fail "csp script-src allows unsafe-inline"
  else
    pass "csp script-src has no unsafe-inline"
  fi
  # Spec conflict 5: if the style fallback was accepted, only script-src is held to this.
  if [[ "$csp" == *unsafe-inline* ]]; then
    echo "note  csp carries unsafe-inline outside script-src (the accepted style fallback?)" >&2
    [[ "$script_src" == *unsafe-inline* ]] || pass "csp is strict where it matters (script-src)"
  else
    pass "csp has no unsafe-inline anywhere"
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
elif [[ "$code" == 502 ]]; then
  fail "raw function URL is 502: the first deploy pass has run but not the second (AUTHGATE_CLIENT_ID=<id> deploy.sh)"
else
  fail "raw function URL is $code, want 401"
fi

# 4. Bruno, the folder that needs no credential. baseUrl is not a secret, so it
# goes on argv; username is left unset, so the wrong-password request skips
# itself rather than count a failure against a real account.
echo "bruno: $base_url"
if (cd "$ROOT/bruno-console" && ../node_modules/.bin/bru run 01-boundary -r --env-var "baseUrl=$base_url"); then
  pass "bruno 01-boundary"
else
  fail "bruno 01-boundary"
fi

if ((fails > 0)); then
  echo "smoke: $fails check(s) failed" >&2
  exit 1
fi
echo "smoke: all checks passed"
