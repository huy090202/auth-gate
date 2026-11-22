#!/usr/bin/env bash
# Hermetic test for 06-prune-clients.sh: a stub `aws` and a stub `curl` first in PATH record
# every call and answer from fixed data. Nothing reaches AWS or the network, and ~/.aws is
# never read. The stub curl fails the run if the admin key shows up in its argv.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/06-prune-clients.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1
export CALLS="$tmp/calls" LEAKS="$tmp/leaks" ADMIN_KEY=sekrit-admin-key-0123456789

# The listing, over two pages: id-A smoke, id-B bruno demo, id-C auth-app-local | id-D auth-app-stg,
# id-console smoke (the Console's own client), and in scenario "portal" also id-portal (named
# bruno demo, the Portal's client). id-FAIL (named smoke, only in scenario "fail") answers 500.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "aws $*" >>"$CALLS"
case "$1 $2" in
  "ssm get-parameter") echo "$ADMIN_KEY" ;;
  "cloudformation describe-stacks")
    stack=""
    prev=""
    for a in "$@"; do
      [[ "$prev" == --stack-name ]] && stack="$a"
      prev="$a"
    done
    case "$stack" in
      auth-gate-t) echo "https://stg.example.test/" ;;
      auth-gate-console-t)
        case "$SCENARIO" in
          warn) echo "warning: something odd, but fine" >&2; echo "id-console" ;;
          console-none) echo None ;;
          console-empty) echo "" ;;
          *) echo "id-console" ;;
        esac ;;
      auth-gate-portal-t)
        case "$SCENARIO" in
          portal) echo "id-portal" ;;
          portal-none) echo None ;;
          portal-denied) echo "An error occurred (AccessDenied) when calling the DescribeStacks operation" >&2; exit 254 ;;
          *) echo "An error occurred (ValidationError) when calling the DescribeStacks operation: Stack with id $stack does not exist" >&2; exit 254 ;;
        esac ;;
      *) echo "stub: unexpected stack $stack" >&2; exit 255 ;;
    esac ;;
  *) echo "stub: unexpected aws call: $*" >&2; exit 255 ;;
esac
STUB
cat >"$tmp/bin/curl" <<'STUB'
#!/usr/bin/env bash
echo "curl $*" >>"$CALLS"
if [[ "$*" == *"$ADMIN_KEY"* ]]; then echo "leak" >>"$LEAKS"; exit 99; fi
method="" out="" url="" prev=""
for a in "$@"; do
  case "$prev" in
    -X) method="$a" ;;
    -o) out="$a" ;;
  esac
  case "$a" in http*) url="$a" ;; esac
  prev="$a"
done
# The key must arrive on stdin, as a header.
header="$(cat)"
if [[ "$header" != "x-internal-access-key: $ADMIN_KEY" ]]; then echo '{}' >"$out"; printf 401; exit 0; fi
c() { printf '{"clientId":"%s","name":"%s","rpId":"%s.example.test","createdAt":"2026-11-01T00:00:00Z"}' "$1" "$2" "$1"; }
case "$method $url" in
  "GET https://stg.example.test/api/clients")
    printf '{"clients":[%s,%s,%s],"nextCursor":"p2+/="}' "$(c id-A smoke)" "$(c id-B "bruno demo")" "$(c id-C auth-app-local)" >"$out"
    printf 200 ;;
  "GET https://stg.example.test/api/clients?cursor=p2%2B%2F%3D")
    extra=""
    [[ "$SCENARIO" == portal ]] && extra=",$(c id-portal "bruno demo")"
    [[ "$SCENARIO" == fail ]] && extra=",$(c id-FAIL smoke)"
    printf '{"clients":[%s,%s%s]}' "$(c id-D auth-app-stg)" "$(c id-console smoke)" "$extra" >"$out"
    printf 200 ;;
  "DELETE https://stg.example.test/api/clients/id-FAIL") echo '{}' >"$out"; printf 500 ;;
  "DELETE https://stg.example.test/api/clients/"*) : >"$out"; printf 204 ;;
  *) echo "stub: unexpected curl: $method $url" >&2; exit 255 ;;
esac
STUB
chmod +x "$tmp/bin/aws" "$tmp/bin/curl"
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

# run <scenario> <answer> [args...]: sets $out (stdout and stderr) and $code.
run() {
  local scenario="$1" answer="$2"
  shift 2
  : >"$CALLS"
  : >"$LEAKS"
  code=0
  out="$(SCENARIO="$scenario" bash "$script" "$@" <<<"$answer" 2>&1)" || code=$?
}
# shellcheck disable=SC2317,SC2329 # called through assert and refute
deleted() { grep -q "^curl .*-X DELETE .*/api/clients/$1\$" "$CALLS"; }
deletes() { grep -c -- '-X DELETE' "$CALLS" || true; }
# shellcheck disable=SC2317,SC2329
no_leak() { [ ! -s "$LEAKS" ]; }

# The listing, over two pages (see the stub).
run none "delete 2"
assert "default: exits 0" test "$code" -eq 0
assert "default: asks for the exact phrase" grep -q "Type 'delete 2' to delete these 2 clients:" <<<"$out"
assert "default: follows the second page" grep -q 'cursor=p2%2B%2F%3D' "$CALLS"
assert "default: asks the stack for the Console's client" grep -q 'describe-stacks .*--stack-name auth-gate-console-t' "$CALLS"
assert "default: deletes the smoke client" deleted id-A
assert "default: deletes the bruno demo client" deleted id-B
assert "default: exactly two deletes" test "$(deletes)" -eq 2
refute "default: never deletes the Console's client" deleted id-console
refute "default: never deletes auth-app-local" deleted id-C
refute "default: never deletes auth-app-stg" deleted id-D
assert "default: prints id, name, rpId and createdAt" grep -qP 'id-A\tsmoke\tid-A.example.test\t2026-11-01T00:00:00Z' <<<"$out"
assert "default: prints deleted ids" grep -q '^deleted id-A$' <<<"$out"
assert "default: the key never reached an argv" no_leak
refute "default: the key is never printed" grep -q "$ADMIN_KEY" <<<"$out"
assert "default: the key reached curl on stdin" grep -q 'curl .* -H @- ' "$CALLS"

run none "delete 1" --keep id-A
assert "--keep: exits 0" test "$code" -eq 0
assert "--keep: proposes 1" grep -q "Type 'delete 1' to delete these 1 clients:" <<<"$out"
assert "--keep: deletes the other one" deleted id-B
refute "--keep: keeps the kept one" deleted id-A

run none "delete 3" --also id-D
assert "--also: proposes 3" grep -q "Type 'delete 3' to delete these 3 clients:" <<<"$out"
assert "--also: deletes the named client" deleted id-D
assert "--also: three deletes" test "$(deletes)" -eq 3

run none "delete 2" --also id-A
assert "--also on a prune-named client: listed once" grep -q "Type 'delete 2' to delete these 2 clients:" <<<"$out"

run none "delete 3" --also id-console
refute "--also Console: exits non-zero" test "$code" -eq 0
assert "--also Console: says why" grep -q 'Console' <<<"$out"
assert "--also Console: no delete" test "$(deletes)" -eq 0
assert "--also Console: stopped before listing" test "$(grep -c 'curl ' "$CALLS" || true)" -eq 0

run none "delete 3" --also id-nope
refute "--also unknown id: exits non-zero" test "$code" -eq 0
assert "--also unknown id: no delete" test "$(deletes)" -eq 0

run none "delete 2" --keep
refute "--keep without an id: exits non-zero" test "$code" -eq 0

run none "yes"
refute "wrong answer: exits non-zero" test "$code" -eq 0
assert "wrong answer: says nothing was deleted" grep -q 'nothing deleted' <<<"$out"
assert "wrong answer: no delete" test "$(deletes)" -eq 0

run none "delete 1"
refute "wrong count: exits non-zero" test "$code" -eq 0
assert "wrong count: no delete" test "$(deletes)" -eq 0

run none ""
refute "empty answer: exits non-zero" test "$code" -eq 0
assert "empty answer: no delete" test "$(deletes)" -eq 0

run portal "delete 2"
assert "portal stack present: exits 0" test "$code" -eq 0
refute "portal stack present: never deletes the Portal's client" deleted id-portal
assert "portal stack present: still deletes the others" deleted id-B
assert "portal stack present: two deletes" test "$(deletes)" -eq 2

run portal "delete 3" --also id-portal
refute "--also Portal: exits non-zero" test "$code" -eq 0
assert "--also Portal: no delete" test "$(deletes)" -eq 0

run portal-denied "delete 2"
refute "portal stack error other than absent: exits non-zero" test "$code" -eq 0
assert "portal stack error: shows it" grep -q AccessDenied <<<"$out"
assert "portal stack error: no delete" test "$(deletes)" -eq 0

run fail "delete 3"
refute "a failed delete: exits non-zero" test "$code" -eq 0
assert "a failed delete: reports it" grep -q 'failed id-FAIL (HTTP 500)' <<<"$out"
assert "a failed delete: the others still deleted" deleted id-A
assert "a failed delete: says how many" grep -q '1 of 3 deletes failed' <<<"$out"

run warn "delete 2"
assert "aws warning on stderr: exits 0" test "$code" -eq 0
refute "aws warning on stderr: the Console's client is still protected" deleted id-console
assert "aws warning on stderr: still two deletes" test "$(deletes)" -eq 2

run console-none "delete 2"
refute "Console id None: exits non-zero" test "$code" -eq 0
assert "Console id None: no delete" test "$(deletes)" -eq 0
run console-empty "delete 2"
refute "Console id empty: exits non-zero" test "$code" -eq 0
assert "Console id empty: no delete" test "$(deletes)" -eq 0

run portal-none "delete 2"
refute "Portal stack without AuthGateClientId: exits non-zero" test "$code" -eq 0
assert "Portal stack without AuthGateClientId: no delete" test "$(deletes)" -eq 0

exit "$fail"
