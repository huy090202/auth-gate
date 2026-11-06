#!/usr/bin/env bash
# Smoke-test a deployed stage: run the Bruno collection, then the real WebAuthn
# round trip (cmd/smoke). Reads BaseUrl and RawVerifyUrl from the stack outputs
# and admin-key from SSM. Never prints a key.
#
# The collection needs Bruno's developer sandbox: 05-tokens/02 verifies the JWT
# with node's crypto, which the default (safe) sandbox does not provide. The
# Bruno CLI also only runs from the collection root, hence the cd.
#
# adminKey must never equal a client key. Two checks make this script fail if it
# does: the test "the client key is never the admin key" in
# bruno/02-clients/01-create.yml, and cmd/smoke's own check on the client it
# creates.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

output() {
  aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text
}

base_url="$(output BaseUrl)"
raw_verify_url="$(output RawVerifyUrl)"
[ -n "$base_url" ] && [ "$base_url" != "None" ] || die "stack $STACK has no BaseUrl output; run 03-deploy.sh"
[ -n "$raw_verify_url" ] && [ "$raw_verify_url" != "None" ] || die "stack $STACK has no RawVerifyUrl output"
base_url="${base_url%/}"
raw_verify_url="${raw_verify_url%/}"

admin_key="$(aws ssm get-parameter --region "$REGION" --name "$PARAM_PREFIX/admin-key" \
  --with-decryption --query Parameter.Value --output text)"
[ -n "$admin_key" ] || die "$PARAM_PREFIX/admin-key is empty"

# A throwaway, gitignored environment file holding the admin key for this run only.
env_file="$ROOT/bruno/environments/$STAGE.yml"
[ ! -e "$env_file" ] || die "$env_file exists; move it away so this run cannot clobber it"
trap 'rm -f "$env_file"' EXIT
(
  umask 077
  cat >"$env_file" <<YAML
name: $STAGE
variables:
  - name: baseUrl
    value: "$base_url"
  - name: rawVerifyUrl
    value: "$raw_verify_url"
  - name: adminKey
    value: "$admin_key"
  - name: subjectId
    value: smoke-bruno-$(date +%s)
YAML
)

echo "bruno: $base_url"
(cd "$ROOT/bruno" && ../node_modules/.bin/bru run --env "$STAGE" --sandbox=developer)

echo "cmd/smoke: $base_url"
(cd "$ROOT" && ADMIN_KEY="$admin_key" go run ./cmd/smoke -base-url "$base_url")
