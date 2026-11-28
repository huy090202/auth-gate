#!/usr/bin/env bash
# Deploy stack auth-gate-portal-$STAGE: write origin-verify if absent, check the Turnstile
# secret, build the Lambda and the web app, sam deploy, upload the site, invalidate.
# Reads AuthGateBaseUrl from the auth-gate stack ($AUTHGATE_STACK). Two passes, as the Console's:
#   pass 1: AUTHGATE_CLIENT_ID empty and the stack not yet there. PublicBaseUrl is empty too,
#           because the CloudFront domain does not exist yet. /api is down (502).
#   between: scripts/portal/register-client.sh stg (needs this stack's BaseUrl).
#   pass 2: AUTHGATE_CLIENT_ID=<printed id> scripts/portal/deploy.sh. PublicBaseUrl is read
#           from this stack's own BaseUrl output, so a later redeploy keeps it.
# The custom domain comes from SSM (scripts/domain/lib.sh): with /credo/auth-gate/$STAGE/domain
# set, the Portal serves on portal.<domain>, PublicBaseUrl is https://portal.<domain> on every
# deploy (no second pass for it), and the deploy stops before sam unless the certificate is
# ISSUED. DNS is never written here. With a domain, SupportEmail=support@<domain> is passed (the
# Reply-To of every email); without one nothing is passed and a previous value is kept.
# Needs EMAIL_FROM (a verified SES identity address) and TURNSTILE_SITE_KEY (public).
# The Turnstile secret lives in SSM $PARAM_PREFIX/turnstile-secret: it is never overwritten;
# when absent, it is written once from TURNSTILE_SECRET (through a 0600 file, never argv).
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
email_from="${EMAIL_FROM:-}"
site_key="${TURNSTILE_SITE_KEY:-}"
[[ -n "$email_from" ]] || die "EMAIL_FROM is required (the SES identity address emails are sent from)"
[[ "$email_from" != *[\<\ ]* ]] || die "EMAIL_FROM must be a bare address (no display name, no '<' or space): the display name is added in code"
[[ -n "$site_key" ]] || die "TURNSTILE_SITE_KEY is required (the Cloudflare Turnstile site key)"

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

# put_param_if_absent <name> <type> <command that prints the value>: writes only when absent. Only
# ParameterNotFound means absent. The value goes producer -> jq stdin -> 0600 file ->
# --cli-input-json, never through any argv.
put_param_if_absent() {
  local name="$1" type="$2"
  shift 2
  local err
  if err="$(aws ssm get-parameter --region "$REGION" --name "$name" 2>&1 >/dev/null)"; then
    echo "kept $name (already exists)"
  elif [[ "$err" == *ParameterNotFound* ]]; then
    (umask 077 && "$@" | jq -R --arg n "$name" --arg t "$type" '{Name: $n, Type: $t, Value: .}' >"$tmp/param.json")
    aws ssm put-parameter --region "$REGION" --cli-input-json "file://$tmp/param.json" >/dev/null
    rm -f "$tmp/param.json"
    echo "wrote $name ($type)"
  else
    die "cannot read $name: $err"
  fi
}

# origin-verify is a plain String: CloudFront origin headers cannot resolve a SecureString.
put_param_if_absent "$PARAM_PREFIX/origin-verify" String openssl rand -hex 32

# The secret is Cloudflare's, so it cannot be generated. Present in SSM: kept. Absent and
# TURNSTILE_SECRET set: written. Neither: stop before building anything.
secret_name="$PARAM_PREFIX/turnstile-secret"
if aws ssm get-parameter --region "$REGION" --name "$secret_name" >/dev/null 2>"$tmp/err"; then
  echo "kept $secret_name (already exists)"
elif grep -q ParameterNotFound "$tmp/err"; then
  [[ -n "${TURNSTILE_SECRET:-}" ]] || die "$secret_name is absent: run once with TURNSTILE_SECRET set (the secret is written to SSM, never printed)"
  put_param_if_absent "$secret_name" SecureString printf '%s\n' "$TURNSTILE_SECRET"
else
  die "cannot read $secret_name: $(<"$tmp/err")"
fi
# The secret is in SSM now (or was already): nothing after this needs it, so make, pnpm and
# sam, and whatever they run, never see it in their environment.
unset TURNSTILE_SECRET

authgate_url="$(output "$AUTHGATE_STACK" BaseUrl)"
[[ -n "$authgate_url" ]] || die "stack $AUTHGATE_STACK has no BaseUrl output; deploy auth-gate first"

# The Portal's own origin: empty while the stack does not exist (pass 1), then its BaseUrl.
public_url=""
if [[ -n "$DOMAIN" ]]; then
  public_url="https://$(app_host portal)"
elif aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" >/dev/null 2>"$tmp/err"; then
  public_url="$(output "$STACK" BaseUrl)"
elif ! grep -q 'does not exist' "$tmp/err"; then
  die "cannot read $STACK: $(<"$tmp/err")"
fi

cd "$ROOT"
make portal-build
pnpm --filter portal-web run build
[[ -f portal-web/dist/index.html ]] || die "portal-web/dist/index.html missing after the build"

# sam rejects empty overrides ("AuthGateClientId="), so an empty value is not passed. On create
# the template default "" applies; on update SAM keeps the previous value, so a plain redeploy
# of a healthy stack keeps its client id (and its public URL, which is re-read above anyway).
params=("Stage=$STAGE" "AuthGateBaseUrl=${authgate_url%/}" "EmailFrom=$email_from" "TurnstileSiteKey=$site_key")
[[ -z "$client_id" ]] || params+=("AuthGateClientId=$client_id")
[[ -z "$public_url" ]] || params+=("PublicBaseUrl=${public_url%/}")
[[ -z "$DOMAIN" ]] || params+=("DomainName=$(app_host portal)" "CertificateArn=$CERT_ARN" "SupportEmail=support@$DOMAIN")
# The WAF switch (read above): always passed, empty when off, so a deploy never leaves a stale web ACL attached.
params+=("WebAclArn=$web_acl_arn")
# The SAM bucket is shared by every project in the account; the deployer may write only
# under auth-gate-* in it (infra/deployer-policy.json).
sam deploy --template-file "$ROOT/infra/portal.yaml" \
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
aws s3 sync "$ROOT/portal-web/dist" "s3://$bucket" --exclude index.html --region "$REGION"
aws s3 cp "$ROOT/portal-web/dist/index.html" "s3://$bucket/index.html" --cache-control no-cache --region "$REGION"
aws s3 sync "$ROOT/portal-web/dist" "s3://$bucket" --delete --exclude index.html --region "$REGION"
aws cloudfront create-invalidation --distribution-id "$dist" --paths '/*' >/dev/null
echo "invalidated $dist"

echo "BaseUrl: $(output "$STACK" BaseUrl)"
echo "RawPortalUrl: $(output "$STACK" RawPortalUrl)"
if [[ -z "$public_url" ]]; then
  echo "PublicBaseUrl was empty on this pass (the domain did not exist): deploy again, after register-client.sh stg, to set it."
fi
# Judge by the deployed value, not the env var: SAM keeps the id of the last deploy.
if [[ -z "$(output "$STACK" AuthGateClientId)" ]]; then
  echo "No AuthGate client id is deployed: /api is down until the second pass. Run scripts/portal/register-client.sh stg, then deploy again with AUTHGATE_CLIENT_ID=<printed id>."
fi
