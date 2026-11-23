#!/usr/bin/env bash
# Hermetic test for ses-identity.sh: a stub `aws` first in PATH records every call. Nothing
# reaches AWS, and ~/.aws is never read.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/ses-identity.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1 CALLS="$tmp/calls"

# Scenarios: new, exists, denied.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$CALLS"
args=" $* "
case "$args" in
  *" sesv2 create-email-identity "*)
    case "$SCENARIO" in
      exists) echo "An error occurred (AlreadyExistsException) when calling the CreateEmailIdentity operation: exists" >&2; exit 254 ;;
      denied) echo "An error occurred (AccessDeniedException) when calling the CreateEmailIdentity operation: no" >&2; exit 254 ;;
    esac ;;
  *" sesv2 get-email-identity "*) printf 'False\tPENDING\n' ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
exit 0
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }
# shellcheck disable=SC2329 # called through the if below
run() {
  export SCENARIO="$1"
  shift
  : >"$CALLS"
  bash "$script" "$@" >"$tmp/out" 2>&1
}
has_call() { grep -qF -- "$1" "$CALLS"; }

if run new example.com; then ok "new: succeeds"; else nok "new: succeeds"; fi
if has_call "--profile default --region ap-southeast-1 sesv2 create-email-identity --email-identity example.com --dkim-signing-attributes NextSigningKeyLength=RSA_2048_BIT"; then
  ok "create: admin profile, app region, RSA_2048_BIT"; else nok "create: admin profile, app region, RSA_2048_BIT"; fi
if grep -q 'VerifiedForSendingStatus: False' "$tmp/out" && grep -q 'DkimAttributes.Status: *PENDING' "$tmp/out"; then
  ok "prints verification and DKIM status"; else nok "prints verification and DKIM status"; fi

if run exists example.com; then ok "AlreadyExists: continues"; else nok "AlreadyExists: continues"; fi
if has_call "sesv2 get-email-identity"; then ok "AlreadyExists: still reads the status"; else nok "AlreadyExists: still reads the status"; fi

if run denied example.com; then nok "other error: dies"; else ok "other error: dies"; fi
if has_call "sesv2 get-email-identity"; then nok "other error: stops before reading"; else ok "other error: stops before reading"; fi
if run new; then nok "no domain: usage error"; else ok "no domain: usage error"; fi

exit "$fail"
