#!/usr/bin/env bash
# Hermetic test for 99-purge.sh: a stub `aws` first in PATH records every call and answers
# from the scenario in $SCENARIO. Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

purge="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/99-purge.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1
export CALLS="$tmp/calls"

# Scenarios: protected, unprotected, absent, describe-denied, update-fails, delete-fails.
# The stub models the table: describe-table answers by the --query it gets. Protection stays on
# for the first poll after update-table and is off from the second, so the script must wait,
# and it must ask exactly "ACTIVE and protection off": any other poll query is an error.
export POLLS="$tmp/polls"
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
query=""
prev=""
for a in "$@"; do
  [[ "$prev" == --query ]] && query="$a"
  prev="$a"
done
case "$1 $2" in
  "dynamodb describe-table")
    case "$SCENARIO" in
      absent) echo "An error occurred (ResourceNotFoundException) when calling the DescribeTable operation" >&2; exit 254 ;;
      describe-denied) echo "An error occurred (AccessDeniedException) when calling the DescribeTable operation" >&2; exit 254 ;;
    esac
    case "$query" in
      Table.DeletionProtectionEnabled)
        if [[ "$SCENARIO" == unprotected ]]; then echo false; else echo true; fi ;;
      "Table.TableStatus=='ACTIVE' && Table.DeletionProtectionEnabled==\`false\`")
        echo x >>"$POLLS"
        if [[ "$(wc -l <"$POLLS")" -ge 2 ]]; then echo true; else echo false; fi ;;
      *) echo "stub: unexpected query: $query" >&2; exit 255 ;;
    esac ;;
  "dynamodb update-table")
    if [[ "$SCENARIO" == update-fails ]]; then
      echo "An error occurred (AccessDeniedException) when calling the UpdateTable operation" >&2
      exit 254
    fi
    echo '{}' ;;
  "cloudformation delete-stack")
    if [[ "$SCENARIO" == delete-fails ]]; then
      echo "An error occurred (AccessDenied) when calling the DeleteStack operation" >&2
      exit 254
    fi ;;
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

# run <scenario> <answer>: sets $out (stdout and stderr) and $code.
run() {
  : >"$CALLS"
  : >"$POLLS"
  code=0
  out="$(SCENARIO="$1" bash "$purge" <<<"$2" 2>&1)" || code=$?
}
# shellcheck disable=SC2317,SC2329 # called through assert and refute
called() { grep -q -- "$1" "$CALLS"; }
# line <pattern> prints the number of the first recorded call that matches.
line() { grep -n -- "$1" "$CALLS" | head -1 | cut -d: -f1; }

run protected wrong
refute "wrong answer: exits non-zero" test "$code" -eq 0
assert "wrong answer: says nothing was deleted" grep -q 'nothing deleted' <<<"$out"
assert "wrong answer: no aws call at all" test ! -s "$CALLS"

run protected auth-gate-t
assert "protected: exits 0" test "$code" -eq 0
assert "protected: turns protection off" called 'dynamodb update-table --region ap-southeast-1 --table-name auth-gate-t --no-deletion-protection-enabled'
assert "protected: turns it off before the stack delete" test "$(line 'update-table')" -lt "$(line 'delete-stack')"
assert "protected: deletes the stack" called 'cloudformation delete-stack --region ap-southeast-1 --stack-name auth-gate-t'
assert "protected: deletes both parameters" test "$(grep -c 'ssm delete-parameter' "$CALLS")" -eq 2
assert "protected: says so" grep -q 'turned off deletion protection on table auth-gate-t' <<<"$out"
assert "protected: waits until protection is off" test "$(wc -l <"$POLLS")" -eq 2

run unprotected auth-gate-t
assert "already unprotected: exits 0" test "$code" -eq 0
refute "already unprotected: no update-table" called 'update-table'
assert "already unprotected: deletes the stack" called 'delete-stack'

run absent auth-gate-t
assert "absent table: exits 0" test "$code" -eq 0
assert "absent table: skips with a message" grep -q 'skip deletion protection (table auth-gate-t absent)' <<<"$out"
refute "absent table: no update-table" called 'update-table'
assert "absent table: still deletes the stack" called 'delete-stack'

run describe-denied auth-gate-t
refute "describe fails: exits non-zero" test "$code" -eq 0
assert "describe fails: shows the error" grep -q 'cannot read table auth-gate-t: .*AccessDeniedException' <<<"$out"
refute "describe fails: stack not deleted" called 'delete-stack'

run update-fails auth-gate-t
refute "update-table fails: exits non-zero" test "$code" -eq 0
assert "update-table fails: shows the error" grep -q 'AccessDeniedException' <<<"$out"
refute "update-table fails: stack not deleted" called 'delete-stack'
refute "update-table fails: parameters kept" called 'ssm delete-parameter'

run delete-fails auth-gate-t
refute "delete-stack fails: exits non-zero" test "$code" -eq 0
assert "delete-stack fails: prints the restore hint" grep -q 'To restore: aws dynamodb update-table --region ap-southeast-1 --table-name auth-gate-t --deletion-protection-enabled' <<<"$out"
assert "delete-stack fails: says the stack was not deleted" grep -q 'stack auth-gate-t was not deleted' <<<"$out"
refute "delete-stack fails: parameters kept" called 'ssm delete-parameter'

exit "$fail"
