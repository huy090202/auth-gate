#!/usr/bin/env bash
# Hermetic test for enable.sh and disable.sh: a stub `aws` records every call and answers from
# $SWITCH_STATE (present|absent), $STACK_STATE (present|absent) and $DISTS. Nothing reaches AWS.
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export ADMIN_PROFILE=stub STAGE=t AWS_REGION=ap-southeast-1 CALLS="$tmp/calls"

cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo " $*" >>"$CALLS"
args=" $* "
case "$args" in
  *" cloudformation "*)
    case "$args" in
      *" --region us-east-1 "*) ;;
      *) echo "stub: cloudformation without --region us-east-1" >&2; exit 255 ;;
    esac ;;
esac
case "$args" in
  *" ssm get-parameter "*)
    if [ "$SWITCH_STATE" = present ]; then echo arn:aws:wafv2:us-east-1:111122223333:global/webacl/x/1
    else echo "An error occurred (ParameterNotFound) when calling the GetParameter operation" >&2; exit 254; fi ;;
  *" cloudformation describe-stacks "*)
    if [ "$STACK_STATE" = present ] || grep -q " cloudformation deploy " "$CALLS"; then echo arn:aws:wafv2:us-east-1:111122223333:global/webacl/x/1
    else echo "An error occurred (ValidationError): Stack with id auth-gate-waf-t does not exist" >&2; exit 254; fi ;;
  *" cloudfront list-distributions-by-web-acl-id "*) printf '%s' "$DISTS" ;;
  *" cloudformation deploy "* | *" ssm put-parameter "* | *" ssm delete-parameter "* \
    | *" cloudformation delete-stack "* | *" cloudformation wait "*) ;;
  *) echo "stub: unexpected call: $args" >&2; exit 255 ;;
esac
exit 0
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
bad() { echo "FAIL - $1" >&2; fail=1; }

# run <script> <stdin> <switch> <stack> <dists>; sets rc and out, resets the call log.
run() {
  : >"$CALLS"
  rc=0
  out="$(SWITCH_STATE="$3" STACK_STATE="$4" DISTS="$5" bash "$dir/$1" <<<"$2" 2>&1)" || rc=$?
}
wrote() { grep -Eq " (cloudformation deploy|ssm put-parameter|ssm delete-parameter|cloudformation delete-stack) " "$CALLS"; }
called() { grep -q -- "$1" "$CALLS"; }

# Why: a stray keystroke must not start billing; only the exact typed line proceeds.
for bad_in in "enable prod" "Enable t" "enable  t" ""; do
  run enable.sh "$bad_in" absent absent ""
  if [ "$rc" -ne 0 ] && ! wrote; then ok "enable rejects '$bad_in'"; else bad "enable accepted '$bad_in'"; fi
done
for bad_in in "disable prod" "Disable t" ""; do
  run disable.sh "$bad_in" present absent ""
  if [ "$rc" -ne 0 ] && ! wrote; then ok "disable rejects '$bad_in'"; else bad "disable accepted '$bad_in'"; fi
done

run enable.sh "enable t" absent absent ""
d="$({ grep -n ' cloudformation deploy ' "$CALLS" || true; } | head -1 | cut -d: -f1)"
r="$({ grep -n ' describe-stacks ' "$CALLS" || true; } | head -1 | cut -d: -f1)"
p="$({ grep -n ' ssm put-parameter ' "$CALLS" || true; } | head -1 | cut -d: -f1)"
if [ "$rc" -eq 0 ] && [ -n "$d" ] && [ -n "$r" ] && [ -n "$p" ] && [ "$d" -lt "$r" ] && [ "$r" -lt "$p" ] \
  && grep -qF "Next: GitHub → Actions → ci → Run workflow (branch main). That deploy attaches the web ACL." <<<"$out"; then
  ok "enable: deploy, read output, put switch, next line"
else bad "enable order or output: rc=$rc $out"; fi
if grep -q "waf-acl-arn" "$CALLS" && ! grep -q "arn:aws:wafv2" <<<"$out"; then ok "enable does not print the ARN"; else bad "enable printed the ARN"; fi

# Stack already exists, switch missing: an interrupted enable can be finished.
run enable.sh "enable t" absent present ""
if [ "$rc" -eq 0 ] && called "ssm put-parameter"; then ok "enable finishes with the stack present"; else bad "enable with stack present: rc=$rc"; fi

run disable.sh "disable t" present present ""
if [ "$rc" -eq 0 ] && called "ssm delete-parameter" && ! called "delete-stack" \
  && grep -qF "Next: run the ci workflow on main to detach the web ACL, then run disable.sh again to delete the stack." <<<"$out"; then
  ok "disable phase 1 deletes the switch only"
else bad "disable phase 1: rc=$rc $out"; fi

# Why: the web ACL must never be deleted while a distribution still uses it.
run disable.sh "disable t" absent present "E123	E456"
if [ "$rc" -eq 1 ] && ! called "delete-stack" && grep -q "E123" <<<"$out"; then
  ok "disable phase 2 refuses while attached"
else bad "disable phase 2 attached: rc=$rc $out"; fi

run disable.sh "disable t" absent present "None"
if [ "$rc" -eq 0 ] && called "delete-stack" && called "wait stack-delete-complete" \
  && grep -qF "WAF off: no web ACL, no WAF cost." <<<"$out"; then
  ok "disable phase 2 deletes the stack when detached"
else bad "disable phase 2 detached: rc=$rc $out"; fi

run disable.sh "" absent absent ""
if [ "$rc" -eq 0 ] && grep -q "already off" <<<"$out" && ! wrote; then ok "disable with nothing is already off"; else bad "already off: rc=$rc $out"; fi

exit "$fail"
