#!/usr/bin/env bash
# Delete the test clients that piled up on a stage (smoke, bruno and probe clients) after a
# typed confirmation. Lists every client through the AuthGate admin API, proposes those
# whose name is in the prune list, and deletes each with DELETE /api/clients/{id}.
#
#   ADMIN_PROFILE=default ./06-prune-clients.sh [--keep <id>]... [--also <id>]...
#
# --keep <id>  never propose this client.
# --also <id>  propose this client too, whatever its name. It must exist.
#
# Never offered, whatever the flags: the Console's own client (output AuthGateClientId of
# auth-gate-console-<stage>) and the Portal's (auth-gate-portal-<stage>, when that stack
# exists). Deleting either would lock that app out, so --also on one of them is refused.
# The admin key is read from SSM and sent only through curl's stdin; it never reaches an
# argv and is never printed.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PRUNE_NAMES=(smoke "bruno boundary A" "bruno boundary B" "bruno boundary C" "bruno demo" "bruno demo renamed" "bruno rate limit" rate-limit-probe)
MAX_PAGES=100

keep=()
also=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --keep | --also)
      [ "$#" -ge 2 ] || die "$1 needs a client id"
      if [ "$1" = --keep ]; then keep+=("$2"); else also+=("$2"); fi
      shift 2 ;;
    *) die "usage: 06-prune-clients.sh [--keep <id>]... [--also <id>]..." ;;
  esac
done

export AWS_PROFILE="${ADMIN_PROFILE:-default}" AWS_REGION="$REGION"

# stack_output <stack> <key>: prints the output value. On failure prints aws's message and returns 1.
stack_output() {
  aws cloudformation describe-stacks --region "$REGION" --stack-name "$1" \
    --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue" --output text 2>&1
}

base_url="$(stack_output "$STACK" BaseUrl)" || die "cannot read BaseUrl of $STACK: $base_url"
[[ -n "$base_url" && "$base_url" != None ]] || die "stack $STACK has no BaseUrl output"
base_url="${base_url%/}"

console_id="$(stack_output "auth-gate-console-$STAGE" AuthGateClientId)" \
  || die "cannot read AuthGateClientId of auth-gate-console-$STAGE: $console_id"
[[ -n "$console_id" && "$console_id" != None ]] || die "auth-gate-console-$STAGE has no AuthGateClientId output, so the Console's client cannot be protected"
protected=("$console_id")

if portal_id="$(stack_output "auth-gate-portal-$STAGE" AuthGateClientId)"; then
  [ -z "$portal_id" ] || [ "$portal_id" = None ] || protected+=("$portal_id")
elif [[ "$portal_id" == *"does not exist"* ]]; then
  echo "skip auth-gate-portal-$STAGE (stack absent)" >&2
else
  die "cannot read AuthGateClientId of auth-gate-portal-$STAGE: $portal_id"
fi

in_list() { # in_list <needle> <items...>
  local n="$1" x
  shift
  for x in "$@"; do [ "$x" = "$n" ] && return 0; done
  return 1
}

for id in ${also[@]+"${also[@]}"}; do
  ! in_list "$id" "${protected[@]}" || die "--also $id is the Console's or the Portal's client; refusing"
done

admin_key="$(aws ssm get-parameter --region "$REGION" --name "$PARAM_PREFIX/admin-key" \
  --with-decryption --query Parameter.Value --output text)"
[ -n "$admin_key" ] || die "$PARAM_PREFIX/admin-key is empty"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# api <METHOD> <path>: the status code in $status, the body in $tmp/body. The key goes in on stdin.
api() {
  status=""
  status="$(printf 'x-internal-access-key: %s\n' "$admin_key" |
    curl -sS --max-time 30 -X "$1" -H @- -o "$tmp/body" -w '%{http_code}' "$base_url$2")" || return 1
}

all='[]'
cursor=""
pages=0
while :; do
  pages=$((pages + 1))
  [ "$pages" -le "$MAX_PAGES" ] || die "more than $MAX_PAGES pages of clients; refusing to go on"
  path=/api/clients
  [ -z "$cursor" ] || path="$path?cursor=$(jq -rn --arg c "$cursor" '$c|@uri')"
  api GET "$path" || die "GET $path failed"
  [ "$status" = 200 ] || die "GET $path answered HTTP $status"
  all="$(jq -c --slurpfile page "$tmp/body" '. + ($page[0].clients // [])' <<<"$all")" || die "GET $path: unreadable answer"
  cursor="$(jq -r '.nextCursor // empty' "$tmp/body")"
  [ -n "$cursor" ] || break
done

json_array() { if [ "$#" -eq 0 ]; then echo '[]'; else printf '%s\n' "$@" | jq -R . | jq -sc .; fi; }
names_json="$(json_array "${PRUNE_NAMES[@]}")"
skip_json="$(json_array "${protected[@]}" ${keep[@]+"${keep[@]}"})"
also_json="$(json_array ${also[@]+"${also[@]}"})"

# A client is proposed when its name is in the prune list and its id is neither protected nor
# kept, or when --also names it. Each id appears once. Tabs and newlines in fields are escaped.
proposal="$(jq -r --argjson names "$names_json" --argjson skip "$skip_json" --argjson also "$also_json" '
  map(select(((.name as $n | $names | index($n)) and (.clientId as $i | $skip | index($i) | not))
             or (.clientId as $i | $also | index($i))))
  | .[] | [.clientId, .name, .rpId, .createdAt] | @tsv' <<<"$all")"

# An --also id that the list does not hold is a typo: refuse before anything is deleted.
for id in ${also[@]+"${also[@]}"}; do
  jq -e --arg id "$id" 'any(.[]; .clientId == $id)' <<<"$all" >/dev/null || die "--also $id: no such client"
done

ids=()
if [ -n "$proposal" ]; then
  while IFS=$'\t' read -r id _; do
    [[ "$id" =~ ^[A-Za-z0-9_-]+$ ]] || die "client id '$id' has unexpected characters; refusing"
    ids+=("$id")
  done <<<"$proposal"
fi
n="${#ids[@]}"
[ "$n" -gt 0 ] || { echo "nothing to delete"; exit 0; }

echo "These clients on $STACK are proposed for deletion:"
printf 'id\tname\trpId\tcreatedAt\n%s\n' "$proposal"
printf "Type 'delete %s' to delete these %s clients: " "$n" "$n" >&2
read -r answer || answer=""
[ -t 0 ] || echo >&2 # piped input leaves the prompt line open
[ "$answer" = "delete $n" ] || die "confirmation did not match; nothing deleted"

failed=0
for id in "${ids[@]}"; do
  if api DELETE "/api/clients/$id" && [ "$status" = 204 ]; then
    echo "deleted $id"
  else
    echo "failed $id (HTTP ${status:-none})" >&2
    failed=$((failed + 1))
  fi
done
[ "$failed" -eq 0 ] || die "$failed of $n deletes failed"
