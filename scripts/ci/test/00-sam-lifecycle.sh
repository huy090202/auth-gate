#!/usr/bin/env bash
# Hermetic test for 00-sam-lifecycle.sh: a stub `aws` first in PATH records every call and
# answers from the scenario in $SCENARIO. Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$here/../00-sam-lifecycle.sh"
rules="$here/../../../infra/sam-lifecycle-rules.json"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub AWS_REGION=ap-southeast-1
export CALLS="$tmp/calls" TMP="$tmp"

# Scenarios: none, foreign, ours-old, get-denied, no-bucket.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
foreign='{"ID":"other-logs","Status":"Enabled","Prefix":"logs/","Expiration":{"Days":3}}'
case "$1 $2" in
  "cloudformation describe-stacks")
    if [[ "$SCENARIO" == no-bucket ]]; then echo None; else echo samclisourcebucket-test; fi ;;
  "s3api get-bucket-lifecycle-configuration")
    case "$SCENARIO" in
      none)
        echo "An error occurred (NoSuchLifecycleConfiguration) when calling the GetBucketLifecycleConfiguration operation: The lifecycle configuration does not exist" >&2
        exit 254 ;;
      get-denied)
        echo "An error occurred (AccessDenied) when calling the GetBucketLifecycleConfiguration operation" >&2
        exit 254 ;;
      foreign)
        echo "{\"TransitionDefaultMinimumObjectSize\":\"varies_by_storage_class\",\"Rules\":[$foreign]}" ;;
      ours-old)
        echo "{\"TransitionDefaultMinimumObjectSize\":\"varies_by_storage_class\",\"Rules\":[$foreign,{\"ID\":\"auth-gate-artifacts-old\",\"Status\":\"Enabled\",\"Filter\":{\"Prefix\":\"auth-gate-\"},\"Expiration\":{\"Days\":90}}]}" ;;
    esac ;;
  "s3api put-bucket-lifecycle-configuration")
    prev=""
    : >"$TMP/size"
    for a in "$@"; do
      [[ "$prev" == --lifecycle-configuration ]] && cp "${a#file://}" "$TMP/put.json"
      [[ "$prev" == --transition-default-minimum-object-size ]] && echo "$a" >"$TMP/size"
      prev="$a"
    done ;;
  *) echo "stub: unexpected call: $*" >&2; exit 255 ;;
esac
exit 0
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "FAIL - $1"; fail=1; }
assert() {
  local msg="$1"
  shift
  if "$@" >/dev/null 2>&1; then ok "$msg"; else nok "$msg"; fi
}

# run <scenario>: sets $code.
run() {
  : >"$CALLS"
  rm -f "$tmp/put.json" "$tmp/size"
  code=0
  SCENARIO="$1" bash "$script" >/dev/null 2>&1 || code=$?
}
put_calls() { grep -c 'put-bucket-lifecycle-configuration' "$CALLS" || true; }
# Compare two JSON documents, ignoring key order.
# shellcheck disable=SC2329  # called through assert
same() { [[ "$(jq -S . <<<"$1")" == "$(jq -S . <<<"$2")" ]]; }
# shellcheck disable=SC2329
profiles_are() { ! grep -qv -- "--profile $1" "$CALLS" && [[ -s "$CALLS" ]]; }

ours="$(cat "$rules")"
foreign='{"ID":"other-logs","Status":"Enabled","Prefix":"logs/","Expiration":{"Days":3}}'

run none
assert "none: exits 0" test "$code" -eq 0
assert "none: puts exactly the rules file" same "$(jq -c . "$tmp/put.json")" "{\"Rules\":$ours}"
assert "none: targets the SAM bucket" grep -q -- '--bucket samclisourcebucket-test' "$CALLS"
assert "none: sends no size flag" test ! -s "$tmp/size"
assert "none: every call uses --profile default" profiles_are default

run foreign
assert "foreign: exits 0" test "$code" -eq 0
assert "foreign: three rules" test "$(jq '.Rules | length' "$tmp/put.json")" -eq 3
assert "foreign: foreign rule first and unchanged" same "$(jq -c '.Rules[0]' "$tmp/put.json")" "$foreign"
assert "foreign: ours follow" same "$(jq -c '.Rules[1:]' "$tmp/put.json")" "$ours"
assert "foreign: size flag kept" grep -qx varies_by_storage_class "$tmp/size"

run ours-old
assert "ours-old: exits 0" test "$code" -eq 0
assert "ours-old: old rule gone" test "$(jq '[.Rules[] | select(.ID == "auth-gate-artifacts-old")] | length' "$tmp/put.json")" -eq 0
assert "ours-old: foreign kept" test "$(jq '[.Rules[] | select(.ID == "other-logs")] | length' "$tmp/put.json")" -eq 1
assert "ours-old: each of ours once" test "$(jq '[.Rules[] | select(.ID | startswith("auth-gate-artifacts-"))] | length' "$tmp/put.json")" -eq 2
assert "ours-old: expire rule present" test "$(jq '[.Rules[] | select(.ID == "auth-gate-artifacts-expire")] | length' "$tmp/put.json")" -eq 1
assert "ours-old: markers rule present" test "$(jq '[.Rules[] | select(.ID == "auth-gate-artifacts-markers")] | length' "$tmp/put.json")" -eq 1

run get-denied
assert "get-denied: exits non-zero" test "$code" -ne 0
assert "get-denied: nothing written" test "$(put_calls)" -eq 0

run no-bucket
assert "no-bucket: exits non-zero" test "$code" -ne 0
assert "no-bucket: nothing written" test "$(put_calls)" -eq 0

: >"$CALLS"
code=0
SCENARIO=foreign ADMIN_PROFILE=other bash "$script" >/dev/null 2>&1 || code=$?
assert "ADMIN_PROFILE: exits 0" test "$code" -eq 0
assert "ADMIN_PROFILE: every call uses --profile other" profiles_are other

exit "$fail"
