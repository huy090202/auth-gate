#!/usr/bin/env bash
# Hermetic test for `register-client.sh stg [--replace]` of the Console and the Portal: stub
# `aws` and `curl` first in PATH, scripts copied into a scratch tree. Nothing reaches AWS, and
# ~/.aws is never read. Calls are recorded; secrets must never appear in any argv.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home" "$tmp/tree/scripts/domain"
cp "$root/scripts/domain/lib.sh" "$tmp/tree/scripts/domain/"
for app in console portal; do
  mkdir -p "$tmp/tree/scripts/$app"
  cp "$root/scripts/$app/lib.sh" "$root/scripts/$app/register-client.sh" "$tmp/tree/scripts/$app/"
done
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub AWS_REGION=ap-southeast-1
ADMIN_SECRET="admin-secret-xyz"
CLIENT_SECRET="client-secret-abc"

# Env: HOST (the app stack's BaseUrl host), STORED (1/0), DOMAINSET (1/0).
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "aws $*" >>"$CALLS"
args=" $* "
notfound() { echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254; }
case "$args" in
  *" sts "*) echo '{}' ;;
  *" ssm get-parameter "*)
    case "$args" in
      *"/credo/auth-gate/stg/domain "*) [ "$DOMAINSET" = 1 ] && echo authgatehq.com || notfound ;;
      *"/credo/auth-gate/stg/certificate-arn "*) echo arn:aws:acm:us-east-1:111122223333:certificate/abc ;;
      *"/credo/auth-gate/stg/admin-key "*) echo "$ADMIN_SECRET" ;;
      *authgate-client-key*) [ "$STORED" = 1 ] && echo '{}' || notfound ;;
      *) echo "stub: unexpected parameter: $args" >&2; exit 255 ;;
    esac ;;
  *" acm describe-certificate "*) echo ISSUED ;;
  *" ssm put-parameter "*) ;;
  *" cloudformation describe-stacks "*)
    case "$args" in
      *"--stack-name auth-gate-stg "*) echo "https://api.example" ;;
      *AuthGateClientId*) echo "old-client-id" ;;
      *) echo "https://$HOST" ;;
    esac ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
STUB
cat >"$tmp/bin/curl" <<'STUB'
#!/usr/bin/env bash
echo "curl $*" >>"$CALLS"
body=""
while [ $# -gt 0 ]; do
  case "$1" in
    --data-binary) body="${2#@}"; shift ;;
    -o) out="$2"; shift ;;
  esac
  shift
done
cp "$body" "$BODY_COPY"
printf '{"clientId":"new-client-id","accessKey":"%s"}' "$CLIENT_SECRET" >"$out"
printf 201
STUB
chmod +x "$tmp/bin/aws" "$tmp/bin/curl"
export PATH="$tmp/bin:$PATH" ADMIN_SECRET CLIENT_SECRET CALLS="$tmp/calls" BODY_COPY="$tmp/body"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }

# run APP ANSWER ARGS...: ANSWER is fed on stdin; calls, output and exit code are kept.
run() {
  local app="$1" answer="$2"
  shift 2
  : >"$CALLS"
  rm -f "$BODY_COPY"
  rc=0
  printf '%s\n' "$answer" | bash "$tmp/tree/scripts/$app/register-client.sh" "$@" >"$tmp/out" 2>&1 || rc=$?
}
posts() { grep -c '^curl ' "$CALLS" || true; }
puts() { grep -c 'ssm put-parameter' "$CALLS" || true; }

for app in console portal; do
  case "$app" in console) host="admin" ;; portal) host="portal" ;; esac
  stack="auth-gate-$app-stg"
  export DOMAINSET=1 STORED=1 HOST="$host.authgatehq.com"

  run "$app" "" stg
  if [ "$rc" -ne 0 ] && [ "$(posts)" -eq 0 ] && [ "$(puts)" -eq 0 ] && grep -q -e "--replace" "$tmp/out"; then
    ok "$app: stored key without --replace refuses, no POST"
  else nok "$app: stored key without --replace (rc=$rc)"; cat "$tmp/out"; fi

  run "$app" "yes" stg --replace
  if [ "$rc" -ne 0 ] && [ "$(posts)" -eq 0 ] && [ "$(puts)" -eq 0 ]; then
    ok "$app: --replace with a wrong answer changes nothing"
  else nok "$app: wrong answer (rc=$rc)"; cat "$tmp/out"; fi

  run "$app" "replace $stack" stg --replace
  if [ "$rc" -eq 0 ] && [ "$(posts)" -eq 1 ] && [ "$(puts)" -eq 1 ] \
    && grep 'ssm put-parameter' "$CALLS" | grep -q -e '--overwrite' \
    && [ "$(jq -r .rpId "$BODY_COPY")" = "$host.authgatehq.com" ] \
    && [ "$(jq -r '.origins[0]' "$BODY_COPY")" = "https://$host.authgatehq.com" ] \
    && grep -q "disable the old client old-client-id in the Console" "$tmp/out" \
    && grep -q "AUTHGATE_CLIENT_ID=new-client-id" "$tmp/out"; then
    ok "$app: --replace with the exact answer: one POST (right rpId, origin), one overwrite, old id printed"
  else nok "$app: exact answer (rc=$rc)"; cat "$CALLS" "$tmp/out"; fi
  if ! grep -q -e "$ADMIN_SECRET" -e "$CLIENT_SECRET" "$CALLS" "$tmp/out"; then
    ok "$app: neither key appears in any argv or in the output"
  else nok "$app: a secret leaked"; cat "$CALLS"; fi

  export HOST="d111.cloudfront.net"
  run "$app" "replace $stack" stg --replace
  if [ "$rc" -ne 0 ] && [ "$(posts)" -eq 0 ] && [ "$(puts)" -eq 0 ] && grep -q "deploy with the domain first" "$tmp/out"; then
    ok "$app: --replace while BaseUrl is still cloudfront.net dies before any POST"
  else nok "$app: cloudfront host not refused (rc=$rc)"; cat "$tmp/out"; fi

  export DOMAINSET=0
  run "$app" "replace $stack" stg --replace
  if [ "$rc" -eq 0 ] && [ "$(posts)" -eq 1 ] && [ "$(jq -r .rpId "$BODY_COPY")" = "d111.cloudfront.net" ]; then
    ok "$app: no domain configured: --replace works without the host check"
  else nok "$app: no-domain replace (rc=$rc)"; cat "$tmp/out"; fi

  export STORED=0 DOMAINSET=1 HOST="$host.authgatehq.com"
  run "$app" "" stg --replace
  if [ "$rc" -eq 0 ] && [ "$(posts)" -eq 1 ] && ! grep -q "Type 'replace" "$tmp/out"; then
    ok "$app: --replace with no stored key registers without a prompt"
  else nok "$app: replace without stored key (rc=$rc)"; cat "$tmp/out"; fi

  run "$app" "" local --replace
  if [ "$rc" -eq 2 ] && [ "$(posts)" -eq 0 ]; then
    ok "$app: local --replace is a usage error (exit 2)"
  else nok "$app: local --replace (rc=$rc)"; fi
done

exit "$fail"
