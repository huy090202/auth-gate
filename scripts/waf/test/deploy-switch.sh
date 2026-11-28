#!/usr/bin/env bash
# Hermetic test for the WAF switch reader (scripts/waf/lib.sh) and for scripts/console/deploy.sh,
# which passes it to sam. Stub `aws`, `make`, `pnpm` and `sam` first in PATH, the scripts copied
# into a scratch tree. Nothing reaches AWS, and ~/.aws is never read.
# Why: a read error must never quietly detach a WAF that is on.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
arn=arn:aws:wafv2:us-east-1:111122223333:global/webacl/auth-gate-t/abc

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home" "$tmp/tree/scripts/console" "$tmp/tree/scripts/domain" \
  "$tmp/tree/scripts/waf" "$tmp/tree/console-web/dist" "$tmp/tree/infra"
cp "$root/scripts/console/lib.sh" "$root/scripts/console/deploy.sh" "$tmp/tree/scripts/console/"
cp "$root/scripts/domain/lib.sh" "$tmp/tree/scripts/domain/"
cp "$root/scripts/waf/lib.sh" "$tmp/tree/scripts/waf/"
: >"$tmp/tree/console-web/dist/index.html"
: >"$tmp/tree/infra/console.yaml"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1

# WAF: off (ParameterNotFound), on (the arn), broken (AccessDeniedException). No domain.
cat >"$tmp/bin/aws" <<STUB
#!/usr/bin/env bash
args=" \$* "
case "\$args" in
  *" ssm get-parameter "*)
    case "\$args" in
      *"/credo/auth-gate/t/waf-acl-arn "*)
        case "\$WAF" in
          on) echo $arn ;;
          broken) echo "An error occurred (AccessDeniedException) when calling the GetParameter operation" >&2; exit 254 ;;
          *) echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254 ;;
        esac ;;
      *"/credo/auth-gate/t/domain "*) echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254 ;;
      *) echo stub ;;
    esac ;;
  *" cloudformation describe-stacks "*) echo "https://out.example" ;;
  *) echo "stub: unexpected call: \$args" >&2; exit 255 ;;
esac
STUB
for tool in make pnpm; do printf '#!/usr/bin/env bash\nexit 0\n' >"$tmp/bin/$tool"; done
# sam records its argv and exits 1, so the script stops right after it.
cat >"$tmp/bin/sam" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$SAM_LOG"
exit 1
STUB
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }

# The helper itself, sourced as the deploy scripts do.
helper() {
  export WAF="$1"
  rc=0
  got="$(bash -c '. "$1/scripts/console/lib.sh"; . "$1/scripts/waf/lib.sh"; waf_acl_arn' _ "$tmp/tree" 2>"$tmp/err")" || rc=$?
}
helper off
if [ "$rc" -eq 0 ] && [ -z "$got" ]; then ok "helper: ParameterNotFound prints nothing and succeeds"; else nok "helper off (rc=$rc, got='$got')"; fi
helper on
if [ "$rc" -eq 0 ] && [ "$got" = "$arn" ]; then ok "helper: prints the arn"; else nok "helper on (rc=$rc, got='$got')"; fi
helper broken
if [ "$rc" -ne 0 ] && [ -z "$got" ] && grep -q AccessDeniedException "$tmp/err"; then ok "helper: any other error fails and says why"; else nok "helper broken (rc=$rc, got='$got')"; fi

# The Console deploy.
run() {
  export WAF="$1" SAM_LOG="$tmp/sam.log"
  : >"$SAM_LOG"
  rc=0
  bash "$tmp/tree/scripts/console/deploy.sh" >"$tmp/out" 2>&1 || rc=$?
}
run off
if [ -s "$SAM_LOG" ] && grep -q -e ' WebAclArn=\(\s\|$\)' "$SAM_LOG"; then ok "console, WAF off: sam gets an empty WebAclArn"; else nok "console, WAF off"; cat "$SAM_LOG" "$tmp/out"; fi
run on
if grep -q "WebAclArn=$arn" "$SAM_LOG"; then ok "console, WAF on: sam gets the web ACL arn"; else nok "console, WAF on"; cat "$SAM_LOG" "$tmp/out"; fi
run broken
if [ "$rc" -ne 0 ] && [ ! -s "$SAM_LOG" ]; then ok "console, switch unreadable: dies before sam deploy"; else nok "console, switch unreadable (rc=$rc)"; cat "$SAM_LOG" "$tmp/out"; fi

exit "$fail"
