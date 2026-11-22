#!/usr/bin/env bash
# Write random admin-key, portal-key (SecureString) and origin-verify (String), each only if absent;
# an existing one is kept and reported as "kept <name>". Rerunnable.
# origin-verify is a plain String: CloudFront origin headers cannot resolve a
# SecureString, and the value sits in clear text in the distribution config anyway.
# Rotate: delete the parameter (99-purge.sh deletes all three with the stack), rerun this script and
# 03-deploy.sh, and the functions pick it up on cold start.
# The value goes openssl -> jq stdin -> 0600 file -> --cli-input-json, never through any argv.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

command -v jq >/dev/null || die "jq is required"
command -v openssl >/dev/null || die "openssl is required"

tmp="$(mktemp -d)"
chmod 700 "$tmp"
trap 'rm -rf "$tmp"' EXIT

for spec in admin-key:SecureString origin-verify:String portal-key:SecureString; do
  name="$PARAM_PREFIX/${spec%%:*}"
  type="${spec#*:}"
  # Only ParameterNotFound means absent; any other failure stops the script.
  if err="$(aws ssm get-parameter --region "$REGION" --name "$name" 2>&1 >/dev/null)"; then
    echo "kept $name (already exists)"
  elif [[ "$err" == *ParameterNotFound* ]]; then
    (umask 077 && openssl rand -hex 32 | jq -R --arg n "$name" --arg t "$type" '{Name: $n, Type: $t, Value: .}' >"$tmp/param.json")
    aws ssm put-parameter --region "$REGION" --cli-input-json "file://$tmp/param.json" >/dev/null
    rm -f "$tmp/param.json"
    echo "wrote $name ($type)"
  else
    die "cannot read $name: $err"
  fi
done
