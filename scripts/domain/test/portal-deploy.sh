#!/usr/bin/env bash
# Hermetic test for the EMAIL_FROM guard of scripts/portal/deploy.sh: a display name or a space is
# refused before anything is deployed; a bare address gets past the guard. Stub `aws` first in
# PATH, the scripts copied into a scratch tree. Nothing reaches AWS, and ~/.aws is never read.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home" "$tmp/tree/scripts/portal" "$tmp/tree/scripts/domain"
cp "$root/scripts/portal/lib.sh" "$root/scripts/portal/deploy.sh" "$tmp/tree/scripts/portal/"
cp "$root/scripts/domain/lib.sh" "$tmp/tree/scripts/domain/"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1 TURNSTILE_SITE_KEY=site

# Every call fails as "no domain parameter"; a bare address then stops later, at the first other call.
cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2
exit 254
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }

for bad in 'AuthGate <no-reply@example.com>' '<no-reply@example.com>' 'no-reply@example.com ' 'a b@example.com'; do
  if EMAIL_FROM="$bad" bash "$tmp/tree/scripts/portal/deploy.sh" >"$tmp/out" 2>&1; then
    nok "refused: '$bad'"
  elif grep -q 'must be a bare address' "$tmp/out"; then
    ok "refused: '$bad'"
  else
    nok "refused with the bare-address message: '$bad'"
  fi
done

EMAIL_FROM=no-reply@example.com bash "$tmp/tree/scripts/portal/deploy.sh" >"$tmp/out" 2>&1 || true
if grep -q 'must be a bare address' "$tmp/out"; then nok "a bare address passes the guard"; else ok "a bare address passes the guard"; fi

exit "$fail"
