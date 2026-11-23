#!/usr/bin/env bash
# Hermetic test for 08-reset-data.sh: a stub `aws` and a stub `go` first in PATH record every
# call. The aws stub answers describe-stacks per stack name and query; the go stub prints a
# fake report. Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/08-reset-data.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub AWS_REGION=ap-southeast-1
export AWS_CALLS="$tmp/aws-calls" GO_CALLS="$tmp/go-calls"

# PORTAL_CLIENT overrides the Portal's AuthGateClientId output (empty and "None" are the failure cases).
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$AWS_CALLS"
stack="" query="" prev=""
for a in "$@"; do
  [[ "$prev" == --stack-name ]] && stack="$a"
  [[ "$prev" == --query ]] && query="$a"
  prev="$a"
done
# The script puts --profile and --region first; the service and operation follow.
args=("$@")
while [[ "${args[0]}" == --profile || "${args[0]}" == --region ]]; do args=("${args[@]:2}"); done
if [[ "${args[0]}" == cloudformation && "${args[1]}" == describe-stacks ]]; then
  case "$stack:$query" in
    auth-gate-console-stg:*"OutputKey=='AuthGateClientId'"*) echo console-id ;;
    auth-gate-portal-stg:*"OutputKey=='AuthGateClientId'"*) echo "${PORTAL_CLIENT-portal-id}" ;;
    auth-gate-portal-stg:*"OutputKey=='AvatarBucket'"*) echo avatar-bucket ;;
    auth-app-stg:*"ParameterKey=='AuthGateClientId'"*) echo app-id ;;
    *) echo "stub: unexpected describe-stacks $stack $query" >&2; exit 255 ;;
  esac
  exit 0
fi
[[ "${args[0]} ${args[1]}" == "s3 rm" ]] && exit 0
echo "stub: unexpected call: $*" >&2
exit 255
STUB
cat >"$tmp/bin/go" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$GO_CALLS"
echo "env profile=${AWS_PROFILE-unset} region=${AWS_REGION-unset} key=${AWS_ACCESS_KEY_ID+set} endpoint=${DYNAMO_ENDPOINT+set}" >>"$GO_CALLS"
[ -z "${GO_FAIL_DRY:-}" ] || case " $* " in *" -apply "*) ;; *) exit 1 ;; esac
echo "keeping clients: console-id portal-id app-id"
echo "total 0 to delete"
case " $* " in *" -apply "*) ;; *) echo "rerun with --apply to delete them" ;; esac
STUB
chmod +x "$tmp/bin/aws" "$tmp/bin/go"
export PATH="$tmp/bin:$PATH"

fail=0
check() { # check <name> <condition...>
  local name="$1"
  shift
  if "$@"; then echo "ok - $name"; else echo "not ok - $name"; fail=1; fi
}
reset_calls() { : >"$AWS_CALLS"; : >"$GO_CALLS"; }
count() { grep -c -- "$1" "$2" || true; }
# run <stdin> <args...>: sets rc and out.
run() {
  local input="$1"
  shift
  rc=0
  out="$(printf '%s' "$input" | bash "$script" "$@" 2>&1)" || rc=$?
}
go_apply_calls() { count ' -apply' "$GO_CALLS"; }
s3_live() { grep -- 's3 rm' "$AWS_CALLS" | grep -vc -- '--dryrun' || true; }
s3_dry() { grep -- 's3 rm' "$AWS_CALLS" | grep -c -- '--dryrun' || true; }

# 1. A stage other than stg is refused before any call.
reset_calls
rc=0
out="$(printf '' | STAGE=prod bash "$script" 2>&1)" || rc=$?
check "STAGE=prod exits non-zero" test "$rc" -ne 0
check "STAGE=prod makes no call" test ! -s "$AWS_CALLS" -a ! -s "$GO_CALLS"

# 2. Empty or None outputs stop before go runs.
for bad in "" None; do
  reset_calls
  rc=0
  out="$(printf '' | PORTAL_CLIENT="$bad" bash "$script" 2>&1)" || rc=$?
  check "portal output '$bad' exits non-zero" test "$rc" -ne 0
  check "portal output '$bad' never runs go" test ! -s "$GO_CALLS"
done

# 3. Dry run: no -apply, every s3 rm has --dryrun, the keep set reaches go.
reset_calls
run ''
check "dry run exits 0" test "$rc" -eq 0
check "dry run: go runs once" test "$(count '-stage' "$GO_CALLS")" -eq 1
check "dry run: go gets no -apply" test "$(go_apply_calls)" -eq 0
check "dry run: go gets the keep set" grep -q -- '-stage stg -console console-id -portal portal-id -authapp app-id' "$GO_CALLS"
check "dry run: two s3 rm --dryrun" test "$(s3_dry)" -eq 2
check "dry run: no s3 rm without --dryrun" test "$(s3_live)" -eq 0
check "dry run: avatars and pending" bash -c "grep -q 's3://avatar-bucket/avatars/' '$AWS_CALLS' && grep -q 's3://avatar-bucket/pending/' '$AWS_CALLS'"

# 4. --apply with a wrong answer deletes nothing.
for answer in $'no\n' ''; do
  reset_calls
  run "$answer" --apply
  check "apply with answer '${answer%$'\n'}' exits non-zero" test "$rc" -ne 0
  check "apply with answer '${answer%$'\n'}': nothing deleted" grep -q 'nothing deleted' <<<"$out"
  check "apply with answer '${answer%$'\n'}': no go -apply" test "$(go_apply_calls)" -eq 0
  check "apply with answer '${answer%$'\n'}': no live s3 rm" test "$(s3_live)" -eq 0
done

# 5. --apply with the exact answer deletes.
reset_calls
run $'reset stg\n' --apply
check "apply exits 0" test "$rc" -eq 0
check "apply: exactly one go -apply" test "$(go_apply_calls)" -eq 1
check "apply: dry run came first" test "$(count '-stage' "$GO_CALLS")" -eq 2
check "apply: two live s3 rm" test "$(s3_live)" -eq 2
check "apply: prints the follow-up" grep -q 'done; run the four smokes' <<<"$out"

# 6. The Go half gets the admin profile and region, without inherited keys or a local endpoint.
reset_calls
rc=0
out="$(printf '' | AWS_ACCESS_KEY_ID=leak AWS_SECRET_ACCESS_KEY=leak AWS_SESSION_TOKEN=leak \
  DYNAMO_ENDPOINT=http://localhost:8000 ADMIN_PROFILE=admin-stub bash "$script" 2>&1)" || rc=$?
check "env: dry run exits 0" test "$rc" -eq 0
check "env: go sees the admin profile" grep -q 'env profile=admin-stub ' "$GO_CALLS"
check "env: go sees the region" grep -q "region=${REGION:-ap-southeast-1} " "$GO_CALLS"
check "env: go sees no AWS_ACCESS_KEY_ID" grep -q 'key= ' "$GO_CALLS"
check "env: go sees no DYNAMO_ENDPOINT" grep -q 'endpoint=$' "$GO_CALLS"

# 7. A failing dry run stops before the prompt, even with --apply and the right answer.
reset_calls
rc=0
out="$(printf 'reset stg\n' | GO_FAIL_DRY=1 bash "$script" --apply 2>&1)" || rc=$?
check "failing dry run exits non-zero" test "$rc" -ne 0
check "failing dry run: no go -apply" test "$(go_apply_calls)" -eq 0
check "failing dry run: no live s3 rm" test "$(s3_live)" -eq 0

# 8. An extra argument is a usage error with no call.
reset_calls
run '' --apply x
check "extra argument exits non-zero" test "$rc" -ne 0
check "extra argument prints usage" grep -q 'usage' <<<"$out"
check "extra argument makes no call" test ! -s "$AWS_CALLS" -a ! -s "$GO_CALLS"

exit "$fail"
