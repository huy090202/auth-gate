#!/usr/bin/env bash
# Hermetic test for scripts/portal/operator-key.sh. A stub `aws` first in PATH: an absent parameter
# is created once (a SecureString, written through a file and never on argv, never printed); an
# existing one is left alone; any other read error stops the script without a write. Nothing
# reaches AWS, and ~/.aws is never read.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/operator-key.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1 AWS_CALLS="$tmp/calls" PUT_COPY="$tmp/put.json"

cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$AWS_CALLS"
case " $* " in
  *" ssm get-parameter "*)
    case "$SCENARIO" in
      exists) echo '{"Parameter":{}}' ;;
      absent) echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254 ;;
      denied) echo "An error occurred (AccessDeniedException) when calling the GetParameter operation" >&2; exit 254 ;;
    esac ;;
  *" ssm put-parameter "*)
    for a in "$@"; do [[ "$a" == file://* ]] && cp "${a#file://}" "$PUT_COPY"; done ;;
  *) echo "stub: unexpected call: $*" >&2; exit 255 ;;
esac
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }
run() { export SCENARIO="$1"; : >"$AWS_CALLS"; rm -f "$PUT_COPY"; bash "$script" >"$tmp/out" 2>"$tmp/err" || { cat "$tmp/err"; return 1; }; }

run absent || nok "absent: exits 0"
if [[ "$(<"$tmp/out")" == created ]]; then ok "absent: prints created"; else nok "absent: prints created"; fi
if jq -e '.Name == "/credo/auth-gate-portal/t/operator-key" and .Type == "SecureString" and (.Value | test("^[0-9a-f]{64}$"))' "$PUT_COPY" >/dev/null; then
  ok "absent: wrote a 64-hex SecureString under the portal prefix"
else
  nok "absent: wrote a 64-hex SecureString under the portal prefix"
fi
value="$(jq -r .Value "$PUT_COPY")"
if grep -qF "$value" "$AWS_CALLS" "$tmp/out" "$tmp/err"; then nok "absent: the key is on argv or printed"; else ok "absent: the key is on no argv and not printed"; fi

run exists || nok "exists: exits 0"
if [[ "$(<"$tmp/out")" == exists ]]; then ok "exists: prints exists"; else nok "exists: prints exists"; fi
if grep -q put-parameter "$AWS_CALLS"; then nok "exists: no write"; else ok "exists: no write"; fi

if run denied; then nok "denied: exits non-zero"; else ok "denied: exits non-zero"; fi
if grep -q put-parameter "$AWS_CALLS"; then nok "denied: no write"; else ok "denied: no write"; fi

exit "$fail"
