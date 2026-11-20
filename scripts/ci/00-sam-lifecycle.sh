#!/usr/bin/env bash
# Add two lifecycle rules for SAM deploy artifacts under the prefix auth-gate- to the SAM
# bucket that `sam deploy --resolve-s3` uses. The bucket is shared with other projects, and
# put-bucket-lifecycle-configuration replaces the whole configuration, so the existing rules
# are read first and kept; only rules whose ID starts with auth-gate-artifacts- are replaced.
# Any read failure stops the script before it writes anything. Rerunnable.
# Run with an admin profile: ADMIN_PROFILE=default ./00-sam-lifecycle.sh
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

command -v jq >/dev/null || die "jq is required"

admin=(--profile "${ADMIN_PROFILE:-default}" --region "$REGION")
rules_file="$ROOT/infra/sam-lifecycle-rules.json"

bucket="$(aws cloudformation describe-stacks "${admin[@]}" --stack-name aws-sam-cli-managed-default \
  --query "Stacks[0].Outputs[?OutputKey=='SourceBucket'].OutputValue | [0]" --output text)" ||
  die "cannot read the stack aws-sam-cli-managed-default"
if [ -z "$bucket" ] || [ "$bucket" = None ]; then
  die "the stack aws-sam-cli-managed-default has no SourceBucket output; run a sam deploy --resolve-s3 first"
fi

expected_bucket=aws-sam-cli-managed-default-samclisourcebucket-m5yrn5zzyiu6
[ "$bucket" = "$expected_bucket" ] ||
  die "the SAM bucket is $bucket but infra/deployer-policy.json pins $expected_bucket; wrong account or region? try AWS_REGION=ap-southeast-1"

err="$(mktemp)"
out="$(mktemp)"
trap 'rm -f "$err" "$out"' EXIT

if current="$(aws s3api get-bucket-lifecycle-configuration "${admin[@]}" --bucket "$bucket" --output json 2>"$err")"; then
  :
elif grep -q NoSuchLifecycleConfiguration "$err"; then
  current='{"Rules":[]}'
else
  cat "$err" >&2
  die "cannot read the lifecycle configuration of $bucket; nothing written"
fi

merged="$(jq --slurpfile ours "$rules_file" \
  '{Rules: (((.Rules // []) | map(select((.ID // "") | startswith("auth-gate-artifacts-") | not))) + $ours[0])}' \
  <<<"$current")"

echo "bucket: $bucket"
# One label per rule, so an empty list and a rule without an ID read differently.
rule_ids='if (.Rules // []) == [] then "(none)" else [.Rules[] | .ID // "(no id)"] | join(", ") end'
echo "rules before: $(jq -r "$rule_ids" <<<"$current")"
echo "rules after:  $(jq -r "$rule_ids" <<<"$merged")"

echo "$merged" >"$out"
extra=()
size="$(jq -r '.TransitionDefaultMinimumObjectSize // empty' <<<"$current")"
[ -z "$size" ] || extra=(--transition-default-minimum-object-size "$size")
aws s3api put-bucket-lifecycle-configuration "${admin[@]}" --bucket "$bucket" \
  --lifecycle-configuration "file://$out" "${extra[@]}"
echo "lifecycle configuration written"
