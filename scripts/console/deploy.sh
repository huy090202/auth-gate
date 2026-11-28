#!/usr/bin/env bash
# Deploy stack auth-gate-console-$STAGE: write origin-verify if absent, build the
# Lambda and the web app, sam deploy, upload the site, invalidate, print the URLs.
# Reads AuthGateBaseUrl from the auth-gate stack ($AUTHGATE_STACK), and PortalUrl and PortalClientId
# from the Portal stack's BaseUrl and AuthGateClientId outputs ($PORTAL_STACK). Order: create the
# operator key first (scripts/portal/operator-key.sh), then deploy the Portal (both passes), then the Console.
# PublicBaseUrl, the avatar bucket's CORS origin, is empty on the first pass (no CloudFront domain
# yet) and read from this stack's own BaseUrl after; with a custom domain it is always set. AUTHGATE_CLIENT_ID
# may be empty on the first pass: deploy, run register-client.sh stg (it needs this
# stack's BaseUrl), deploy again with AUTHGATE_CLIENT_ID set. CONSOLE_RESERVED_CONCURRENCY,
# when set, reserves that many concurrent executions for the Console.
# The custom domain comes from SSM (scripts/domain/lib.sh): with /credo/auth-gate/$STAGE/domain
# set, the Console serves on admin.<domain>, and the deploy stops before sam unless the
# certificate is ISSUED. DNS is never written here.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source=../domain/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../domain/lib.sh"

# shellcheck source=../waf/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../waf/lib.sh"

domain_config
# Read the WAF switch once, before any build: a read error stops the deploy here.
web_acl_arn="$(waf_acl_arn)" || exit 1
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

portal_url="$(output "$PORTAL_STACK" BaseUrl)"
portal_client_id="$(output "$PORTAL_STACK" AuthGateClientId)"
[[ -n "$portal_url" ]] || die "stack $PORTAL_STACK has no BaseUrl output; deploy the Portal first"
[[ -n "$portal_client_id" ]] || die "stack $PORTAL_STACK has no AuthGateClientId output; finish the Portal's second deploy pass first"

# The Console's own origin: empty while the stack does not exist (pass 1), then its BaseUrl.
public_url=""
if [[ -n "$DOMAIN" ]]; then
  public_url="https://$(app_host admin)"
elif aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" >/dev/null 2>"$tmp/err"; then
  public_url="$(output "$STACK" BaseUrl)"
elif ! grep -q 'does not exist' "$tmp/err"; then
  die "cannot read $STACK: $(<"$tmp/err")"
fi

cd "$ROOT"
make console-build
pnpm --filter console-web run build
[[ -f console-web/dist/index.html ]] || die "console-web/dist/index.html missing after the build"

# sam rejects empty overrides ("AuthGateClientId="), so the id is passed only when set.
# On create the template default "" applies; on update SAM keeps the previous value,
# so a plain redeploy of a healthy stack keeps its client id.
params=("Stage=$STAGE" "AuthGateBaseUrl=${authgate_url%/}" "PortalUrl=${portal_url%/}" "PortalClientId=$portal_client_id")
[[ -z "$public_url" ]] || params+=("PublicBaseUrl=${public_url%/}")
[[ -z "$client_id" ]] || params+=("AuthGateClientId=$client_id")
[[ -z "$DOMAIN" ]] || params+=("DomainName=$(app_host admin)" "CertificateArn=$CERT_ARN")
# The WAF switch (read above): always passed, none when off. sam rejects empty overrides and an omitted one keeps the previous value, so only the sentinel detaches a web ACL.
params+=("WebAclArn=${web_acl_arn:-none}")
# The same for CONSOLE_RESERVED_CONCURRENCY (0 reserves none): unset, a create gets the
# template default 0 and an update keeps the previous value.
if [[ -n "${CONSOLE_RESERVED_CONCURRENCY:-}" ]]; then
  [[ "$CONSOLE_RESERVED_CONCURRENCY" =~ ^[0-9]+$ ]] || die "CONSOLE_RESERVED_CONCURRENCY must be a whole number"
  params+=("ReservedConcurrency=$CONSOLE_RESERVED_CONCURRENCY")
fi
# The SAM bucket is shared by every project in the account; the deployer may write only
# under auth-gate-* in it (infra/deployer-policy.json).
sam deploy --template-file "$ROOT/infra/console.yaml" \
  --stack-name "$STACK" --region "$REGION" \
  --capabilities CAPABILITY_IAM --resolve-s3 --s3-prefix "$STACK" \
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
if [[ -z "$public_url" ]]; then
  echo "PublicBaseUrl was empty on this pass (the domain did not exist): deploy again to set it; avatar uploads fail on CORS until then."
fi
# Judge by the deployed value, not the env var: SAM keeps the id of the last deploy.
if [[ -z "$(output "$STACK" AuthGateClientId)" ]]; then
  echo "No AuthGate client id is deployed: /api is down until the second pass. Run register-client.sh stg, then deploy again with AUTHGATE_CLIENT_ID=<printed id>."
fi
