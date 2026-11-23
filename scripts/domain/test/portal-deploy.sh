#!/usr/bin/env bash
# Hermetic test for scripts/portal/deploy.sh. The EMAIL_FROM guard: a display name or a space is
# refused before anything is deployed; a bare address gets past the guard. The support address:
# with a domain, sam gets SupportEmail=support@<domain>; without one, no SupportEmail override.
# Stub `aws`, `make`, `pnpm` and `sam` first in PATH, the scripts copied into a scratch tree.
# Nothing reaches AWS, and ~/.aws is never read.
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

# Full stubs: SSM answers the domain (scenario "domain") or ParameterNotFound for it ("absent"),
# every other parameter exists. sam records its argv and exits 1, so the script stops after it.
mkdir -p "$tmp/full"
cat >"$tmp/full/aws" <<'STUB'
#!/usr/bin/env bash
args=" $* "
case "$args" in
  *" ssm get-parameter "*)
    case "$args" in
      *"/credo/auth-gate/t/domain "*)
        if [ "$SCENARIO" = absent ]; then
          echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254
        fi
        echo authgatehq.com ;;
      *"/credo/auth-gate/t/certificate-arn "*) echo arn:aws:acm:us-east-1:111122223333:certificate/abc ;;
      *) echo stub ;;
    esac ;;
  *" acm describe-certificate "*) echo ISSUED ;;
  *" cloudformation describe-stacks "*) echo "https://out.example" ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
STUB
for tool in make pnpm; do printf '#!/usr/bin/env bash\nexit 0\n' >"$tmp/full/$tool"; done
cat >"$tmp/full/sam" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$SAM_LOG"
exit 1
STUB
chmod +x "$tmp/full/"*
mkdir -p "$tmp/tree/portal-web/dist" "$tmp/tree/infra"
: >"$tmp/tree/portal-web/dist/index.html"
: >"$tmp/tree/infra/portal.yaml"

run_full() {
  export SCENARIO="$1" SAM_LOG="$tmp/sam.log"
  : >"$SAM_LOG"
  PATH="$tmp/full:$PATH" EMAIL_FROM=no-reply@example.com bash "$tmp/tree/scripts/portal/deploy.sh" >"$tmp/out" 2>&1 || true
}

run_full domain
if grep -q 'SupportEmail=support@authgatehq.com' "$SAM_LOG"; then ok "domain: sam gets SupportEmail=support@<domain>"; else nok "domain: no SupportEmail override"; cat "$SAM_LOG" "$tmp/out"; fi

run_full absent
if [ ! -s "$SAM_LOG" ]; then nok "no domain: sam was not reached"; cat "$tmp/out"
elif grep -q 'SupportEmail=' "$SAM_LOG"; then nok "no domain: SupportEmail must not be passed"; cat "$SAM_LOG"
else ok "no domain: no SupportEmail override"; fi

exit "$fail"
