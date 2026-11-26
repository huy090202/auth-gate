#!/usr/bin/env bash
# Create the operator key if absent: SecureString /credo/auth-gate-portal/$STAGE/operator-key,
# which the Portal checks on /api/operator/* (header x-operator-key) and the Console sends. It is
# never overwritten and never printed; prints `created` or `exists`. Only ParameterNotFound means
# absent. The value goes openssl -> jq stdin -> 0600 file -> --cli-input-json, never through any argv.
# Run once per stage, before the first Portal or Console deploy that needs it (CI's preflight.sh
# fails without it).
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v jq >/dev/null || die "jq is required"
command -v openssl >/dev/null || die "openssl is required"

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

name="$PARAM_PREFIX/operator-key"
if err="$(aws ssm get-parameter --region "$REGION" --name "$name" 2>&1 >/dev/null)"; then
  echo exists
elif [[ "$err" == *ParameterNotFound* ]]; then
  (umask 077 && openssl rand -hex 32 | jq -R --arg n "$name" '{Name: $n, Type: "SecureString", Value: .}' >"$tmp/param.json")
  aws ssm put-parameter --region "$REGION" --cli-input-json "file://$tmp/param.json" >/dev/null
  echo created
else
  die "cannot read $name: $err"
fi
