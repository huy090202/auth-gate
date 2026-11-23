#!/usr/bin/env bash
# Register the Portal as a client of auth-gate: POST /api/clients with the admin key.
#   local: rpId localhost, origin http://localhost:5174; writes .env.portal
#          (AUTHGATE_CLIENT_ID, CLIENT_KEY, PORTAL_KEY=dev-portal) for make portal-dev.
#   stg:   rpId and origin from the stack's BaseUrl; writes the key to SSM
#          (SecureString) and prints the client id for deploy.
# The admin key is read from SSM (stg) or is dev-admin (local), and used only for this
# registration: the Portal never holds it. The client key is
# shown once by auth-gate, so this refuses to overwrite a key that is already stored.
#   stg --replace: for a new rpId (the custom domain). AuthGate refuses an rpId change, so this
#          registers a NEW client and overwrites the stored key, after a typed confirmation read
#          from stdin. With a domain configured the stack's BaseUrl must already be on it.
set -euo pipefail
usage() { echo "usage: $0 stg [--replace] | local" >&2; exit 2; }
target="${1:-}"
replace=0
case "$target" in
  local) [[ $# -eq 1 ]] || usage; STAGE=local ;;
  stg)
    case "${2:-}" in
      "") [[ $# -eq 1 ]] || usage ;;
      --replace) [[ $# -eq 2 ]] || usage; replace=1 ;;
      *) usage ;;
    esac
    STAGE=stg ;;
  *) usage ;;
esac
export STAGE
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source=../domain/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../domain/lib.sh"

command -v jq >/dev/null || die "jq is required"
env_file="$ROOT/.env.portal"
name="auth-gate-portal-$STAGE"

if [[ "$target" == local ]]; then
  base="${AUTHGATE_BASE_URL:-http://localhost:8080}"
  admin_key="dev-admin"
  if [[ -f "$env_file" ]] && grep -Eq '^CLIENT_KEY=.+' "$env_file"; then
    die "$env_file already has CLIENT_KEY; remove it first to register again"
  fi
  rp_id="localhost"
  origin="http://localhost:5174"
else
  aws sts get-caller-identity --region "$REGION" >/dev/null || die "no AWS credentials for profile $AWS_PROFILE"
  # Only ParameterNotFound means absent; any other error stops before registering.
  stored=0
  if err="$(aws ssm get-parameter --region "$REGION" --name "$PARAM_PREFIX/authgate-client-key" 2>&1 >/dev/null)"; then
    [[ "$replace" == 1 ]] || die "$PARAM_PREFIX/authgate-client-key already exists; to register a new client for a new rpId run: $0 stg --replace"
    stored=1
  elif [[ "$err" != *ParameterNotFound* ]]; then
    die "cannot read $PARAM_PREFIX/authgate-client-key: $err"
  fi
  old_id=""
  if [[ "$stored" == 1 ]]; then
    domain_config
    old_id="$(output "$STACK" AuthGateClientId)" || old_id=""
    if [[ -n "$DOMAIN" ]]; then
      want="$(app_host portal)"
      have="$(output "$STACK" BaseUrl)" || have=""
      have="${have#*://}"
      have="${have%%[/:]*}"
      [[ "$have" == "$want" ]] || die "stack $STACK BaseUrl host is '${have:-none}', not $want: deploy with the domain first"
    fi
    printf "Type 'replace %s' to register a new client and overwrite the stored key: " "$STACK" >&2
    answer=""
    read -r answer || true
    [[ "$answer" == "replace $STACK" ]] || die "not confirmed; nothing changed"
  fi
  base="$(output "$AUTHGATE_STACK" BaseUrl)"
  [[ -n "$base" ]] || die "stack $AUTHGATE_STACK has no BaseUrl output"
  admin_key="$(aws ssm get-parameter --region "$REGION" --name "/credo/auth-gate/$STAGE/admin-key" \
    --with-decryption --query Parameter.Value --output text)"
  [[ -n "$admin_key" ]] || die "/credo/auth-gate/$STAGE/admin-key is empty"
  origin="$(output "$STACK" BaseUrl)"
  [[ -n "$origin" ]] || die "stack $STACK has no BaseUrl output; deploy it first"
  origin="${origin%/}"
  rp_id="${origin#*://}"
  rp_id="${rp_id%%[/:]*}"
fi
base="${base%/}"
# make dev serves the admin role under /admin; on AWS the admin API is at the base URL.
api="$base"
[[ "$target" == local ]] && api="$base/admin"

# Secrets go through 0600 temp files, never argv.
tmpdir="$(mktemp -d)"
chmod 700 "$tmpdir"
trap 'rm -rf "$tmpdir"' EXIT
printf 'x-internal-access-key: %s\n' "$admin_key" >"$tmpdir/headers"
jq -n --arg name "$name" --arg rp "$rp_id" --arg origin "$origin" \
  '{name: $name, rpId: $rp, origins: [$origin]}' >"$tmpdir/body"
status="$(curl -sS --connect-timeout 5 --max-time 30 -o "$tmpdir/resp" -w '%{http_code}' -X POST "$api/api/clients" \
  -H @"$tmpdir/headers" -H 'content-type: application/json' \
  --data-binary @"$tmpdir/body")" || {
  if [[ "$target" == local ]]; then
    die "POST $api/api/clients: no answer. Is AuthGate running? Start it with: make dev"
  fi
  die "POST $api/api/clients: network error"
}
if [[ "$status" != 201 ]]; then
  # Status and error code only: the body is not echoed.
  die "POST $api/api/clients -> $status $(jq -r '.error // empty' "$tmpdir/resp" 2>/dev/null || true)"
fi
client_id="$(jq -r '.clientId // empty' "$tmpdir/resp")"
access_key="$(jq -r '.accessKey // empty' "$tmpdir/resp")"
[[ -n "$client_id" && -n "$access_key" ]] || die "unexpected response from auth-gate"

if [[ "$target" == local ]]; then
  touch "$env_file"
  tmp="$(mktemp)"
  chmod 600 "$tmp"
  grep -Ev '^(AUTHGATE_CLIENT_ID|CLIENT_KEY|PORTAL_KEY)=' "$env_file" >"$tmp" || true
  {
    echo "AUTHGATE_CLIENT_ID=$client_id"
    echo "CLIENT_KEY=$access_key"
    echo "PORTAL_KEY=dev-portal"
  } >>"$tmp"
  mv "$tmp" "$env_file"
  chmod 600 "$env_file"
  echo "registered $name ($client_id); wrote $env_file"
else
  # The key goes from the response file to the param file inside jq, never on argv.
  jq --arg n "$PARAM_PREFIX/authgate-client-key" \
    '{Name: $n, Type: "SecureString", Value: .accessKey}' "$tmpdir/resp" >"$tmpdir/param"
  overwrite=()
  [[ "$replace" == 1 ]] && overwrite=(--overwrite)
  if ! aws ssm put-parameter --region "$REGION" ${overwrite[@]+"${overwrite[@]}"} --cli-input-json "file://$tmpdir/param" >/dev/null; then
    {
      echo "error: registered client $client_id but could not write $PARAM_PREFIX/authgate-client-key."
      echo "The client key is not recoverable from auth-gate. Rotate it with"
      echo "  POST $base/api/clients/$client_id/keys (admin key header)"
      echo "and store the returned accessKey in $PARAM_PREFIX/authgate-client-key (SecureString)."
    } >&2
    exit 1
  fi
  echo "registered $name; wrote $PARAM_PREFIX/authgate-client-key (SecureString)"
  if [[ "$replace" == 1 ]]; then
    if [[ -n "$old_id" ]]; then
      echo "disable the old client $old_id in the Console once the new one works"
    else
      echo "disable the old client in the Console once the new one works (its id could not be read from $STACK)"
    fi
  fi
  echo "AUTHGATE_CLIENT_ID=$client_id   # pass this to the portal deploy"
fi
