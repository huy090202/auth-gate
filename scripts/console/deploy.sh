#!/usr/bin/env bash
# Deploy stack auth-gate-console-$STAGE: write origin-verify if absent, build the
# Lambda and the web app, sam deploy, upload the site, invalidate, print the URLs.
# Reads AuthGateBaseUrl from the auth-gate stack ($AUTHGATE_STACK). AUTHGATE_CLIENT_ID
# may be empty on the first pass: deploy, run register-client.sh stg (it needs this
# stack's BaseUrl), deploy again with AUTHGATE_CLIENT_ID set. CONSOLE_RESERVED_CONCURRENCY,
# when set, reserves that many concurrent executions for the Console.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v jq >/dev/null || die "jq is required"
command -v openssl >/dev/null || die "openssl is required"
client_id="${AUTHGATE_CLIENT_ID:-}"

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

# origin-verify is a plain String: CloudFront origin headers cannot resolve a
# SecureString. Never overwritten: only ParameterNotFound means absent. The value
# goes openssl -> jq stdin -> 0600 file -> --cli-input-json, never through any argv.
name="$PARAM_PREFIX/origin-verify"
if err="$(aws ssm get-parameter --region "$REGION" --name "$name" 2>&1 >/dev/null)"; then
  echo "kept $name (already exists)"
elif [[ "$err" == *ParameterNotFound* ]]; then
  (umask 077 && openssl rand -hex 32 | jq -R --arg n "$name" '{Name: $n, Type: "String", Value: .}' >"$tmp/param.json")
  aws ssm put-parameter --region "$REGION" --cli-input-json "file://$tmp/param.json" >/dev/null
  rm -f "$tmp/param.json"
  echo "wrote $name (String)"
else
  die "cannot read $name: $err"
fi

authgate_url="$(output "$AUTHGATE_STACK" BaseUrl)"
[[ -n "$authgate_url" ]] || die "stack $AUTHGATE_STACK has no BaseUrl output; deploy auth-gate first"

cd "$ROOT"
make console-build
pnpm --filter console-web run build
[[ -f console-web/dist/index.html ]] || die "console-web/dist/index.html missing after the build"

# sam rejects empty overrides ("AuthGateClientId="), so the id is passed only when set.
# On create the template default "" applies; on update SAM keeps the previous value,
# so a plain redeploy of a healthy stack keeps its client id.
params=("Stage=$STAGE" "AuthGateBaseUrl=${authgate_url%/}")
[[ -z "$client_id" ]] || params+=("AuthGateClientId=$client_id")
# The same for CONSOLE_RESERVED_CONCURRENCY (0 reserves none): unset, a create gets the
# template default 0 and an update keeps the previous value.
if [[ -n "${CONSOLE_RESERVED_CONCURRENCY:-}" ]]; then
  [[ "$CONSOLE_RESERVED_CONCURRENCY" =~ ^[0-9]+$ ]] || die "CONSOLE_RESERVED_CONCURRENCY must be a whole number"
  params+=("ReservedConcurrency=$CONSOLE_RESERVED_CONCURRENCY")
fi
sam deploy --template-file "$ROOT/infra/console.yaml" \
  --stack-name "$STACK" --region "$REGION" \
  --capabilities CAPABILITY_IAM --resolve-s3 \
  --no-confirm-changeset --no-fail-on-empty-changeset \
  --parameter-overrides "${params[@]}"

bucket="$(output "$STACK" SiteBucket)"
dist="$(output "$STACK" DistributionId)"
[[ -n "$bucket" && -n "$dist" ]] || die "stack $STACK has no SiteBucket or DistributionId output"
# Order matters. New hashed assets go up first, then the shell (index.html, never
# cached by browsers), then the old assets are deleted: a client mid-load never
# finds a missing file, and a stale shell cannot outlive its assets for long.
aws s3 sync "$ROOT/console-web/dist" "s3://$bucket" --exclude index.html --region "$REGION"
aws s3 cp "$ROOT/console-web/dist/index.html" "s3://$bucket/index.html" --cache-control no-cache --region "$REGION"
aws s3 sync "$ROOT/console-web/dist" "s3://$bucket" --delete --exclude index.html --region "$REGION"
aws cloudfront create-invalidation --distribution-id "$dist" --paths '/*' >/dev/null
echo "invalidated $dist"

echo "BaseUrl: $(output "$STACK" BaseUrl)"
echo "RawConsoleUrl: $(output "$STACK" RawConsoleUrl)"
# Judge by the deployed value, not the env var: SAM keeps the id of the last deploy.
if [[ -z "$(output "$STACK" AuthGateClientId)" ]]; then
  echo "No AuthGate client id is deployed: /api is down until the second pass. Run register-client.sh stg, then deploy again with AUTHGATE_CLIENT_ID=<printed id>."
fi
