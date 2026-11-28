#!/usr/bin/env bash
# Owner-run, once: create the cost budget `auth-gate-monthly` (COST, MONTHLY, 10 USD) with email
# alerts at FORECASTED > 80% and ACTUAL > 100%. BUDGET_EMAIL (required) is the alert address. It is
# never on any argv and never printed: it goes env -> jq -> 0600 file -> --cli-input-json.
# Prints `created` or `exists`. Only NotFoundException means absent. Budgets is a global service.
# Run with admin credentials: ADMIN_PROFILE (default `default`).
set -euo pipefail

die() { echo "error: $*" >&2; exit 1; }

[[ -n "${BUDGET_EMAIL:-}" ]] || die "BUDGET_EMAIL is required (the address for the cost alerts)"
command -v jq >/dev/null || die "jq is required"

export AWS_PROFILE="${ADMIN_PROFILE:-default}"
name=auth-gate-monthly

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

account="$(aws sts get-caller-identity --query Account --output text)"

if err="$(aws budgets describe-budget --region us-east-1 --account-id "$account" --budget-name "$name" 2>&1 >/dev/null)"; then
  echo exists
elif [[ "$err" == *NotFoundException* ]]; then
  (umask 077 && jq -n --arg a "$account" --arg n "$name" '
    def alert($type; $threshold): {
      Notification: {NotificationType: $type, ComparisonOperator: "GREATER_THAN", Threshold: $threshold, ThresholdType: "PERCENTAGE"},
      Subscribers: [{SubscriptionType: "EMAIL", Address: env.BUDGET_EMAIL}]
    };
    {
      AccountId: $a,
      Budget: {BudgetName: $n, BudgetLimit: {Amount: "10", Unit: "USD"}, TimeUnit: "MONTHLY", BudgetType: "COST"},
      NotificationsWithSubscribers: [alert("FORECASTED"; 80), alert("ACTUAL"; 100)]
    }' >"$tmp/budget.json")
  aws budgets create-budget --region us-east-1 --cli-input-json "file://$tmp/budget.json" >/dev/null
  echo created
else
  die "cannot read budget $name: ${err//"$BUDGET_EMAIL"/<redacted>}"
fi
