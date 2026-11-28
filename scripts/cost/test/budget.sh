#!/usr/bin/env bash
# Hermetic test for scripts/cost/budget.sh. A stub `aws` first in PATH records argv: without
# BUDGET_EMAIL nothing is called; an absent budget is created once from a JSON file; an existing
# one is left alone; the address is on no argv and in no output. Nothing reaches AWS, and ~/.aws
# is never read.
set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/budget.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/home"
export HOME="$tmp/home" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
export AWS_CALLS="$tmp/calls" CREATE_COPY="$tmp/create.json"
addr="owner-secret@example.invalid"

cat >"$tmp/bin/aws" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$AWS_CALLS"
case " $* " in
  *" sts get-caller-identity "*) echo 123456789012 ;;
  *" budgets describe-budget "*)
    case "$SCENARIO" in
      exists) echo '{"Budget":{}}' ;;
      absent) echo "An error occurred (NotFoundException) when calling the DescribeBudget operation" >&2; exit 254 ;;
      createfail) echo "An error occurred (NotFoundException) when calling the DescribeBudget operation" >&2; exit 254 ;;
      denied) echo "An error occurred (AccessDeniedException) when calling the DescribeBudget operation" >&2; exit 254 ;;
    esac ;;
  *" budgets create-budget "*)
    if [ "$SCENARIO" = createfail ]; then echo "An error occurred (AccessDenied): cannot notify $BUDGET_EMAIL" >&2; exit 254; fi
    for a in "$@"; do [[ "$a" == file://* ]] && cp "${a#file://}" "$CREATE_COPY"; done ;;
  *) echo "stub: unexpected call: $*" >&2; exit 255 ;;
esac
STUB
chmod +x "$tmp/bin/aws"
export PATH="$tmp/bin:$PATH"

fail=0
ok() { echo "ok - $1"; }
nok() { echo "not ok - $1"; fail=1; }
run() { export SCENARIO="$1"; : >"$AWS_CALLS"; rm -f "$CREATE_COPY"; bash "$script" >"$tmp/out" 2>"$tmp/err"; }

# Why: the owner's address must not land in shell history, CI logs or the repo.
unset BUDGET_EMAIL
if run absent; then nok "no address: exits non-zero"; else ok "no address: exits non-zero"; fi
if [[ -s "$AWS_CALLS" ]]; then nok "no address: no aws call"; else ok "no address: no aws call"; fi

export BUDGET_EMAIL="$addr"
run absent || { cat "$tmp/err"; nok "absent: exits 0"; }
if [[ "$(<"$tmp/out")" == created ]]; then ok "absent: prints created"; else nok "absent: prints created"; fi
if jq -e '
  .AccountId == "123456789012"
  and .Budget.BudgetName == "auth-gate-monthly"
  and .Budget.BudgetLimit == {Amount: "10", Unit: "USD"}
  and .Budget.TimeUnit == "MONTHLY" and .Budget.BudgetType == "COST"
  and ([.NotificationsWithSubscribers[] | [.Notification.NotificationType, .Notification.ComparisonOperator, .Notification.Threshold, .Notification.ThresholdType,
        (.Subscribers | map([.SubscriptionType, .Address]))]]
      == [["FORECASTED","GREATER_THAN",80,"PERCENTAGE",[["EMAIL","'"$addr"'"]]],
          ["ACTUAL","GREATER_THAN",100,"PERCENTAGE",[["EMAIL","'"$addr"'"]]]])
' "$CREATE_COPY" >/dev/null; then ok "absent: budget JSON is 10 USD monthly with both alerts"; else nok "absent: budget JSON is 10 USD monthly with both alerts"; fi
if grep -qF "$addr" "$AWS_CALLS" "$tmp/out" "$tmp/err"; then nok "absent: the address is on argv or printed"; else ok "absent: the address is on no argv and not printed"; fi

run exists || nok "exists: exits 0"
if [[ "$(<"$tmp/out")" == exists ]]; then ok "exists: prints exists"; else nok "exists: prints exists"; fi
if grep -q create-budget "$AWS_CALLS"; then nok "exists: no create"; else ok "exists: no create"; fi
if grep -qF "$addr" "$AWS_CALLS" "$tmp/out" "$tmp/err"; then nok "exists: the address is on argv or printed"; else ok "exists: the address is on no argv and not printed"; fi

if run denied; then nok "denied: exits non-zero"; else ok "denied: exits non-zero"; fi
if grep -q create-budget "$AWS_CALLS"; then nok "denied: no create"; else ok "denied: no create"; fi

# Why: AWS may echo the subscriber address in a create error; it must not reach the terminal or logs.
if run createfail; then nok "createfail: exits non-zero"; else ok "createfail: exits non-zero"; fi
if grep -qF "$addr" "$tmp/out" "$tmp/err"; then nok "createfail: the address is printed"; else ok "createfail: the address is redacted"; fi
if grep -q "<redacted>" "$tmp/err"; then ok "createfail: error is shown"; else nok "createfail: error is shown"; fi

exit "$fail"
