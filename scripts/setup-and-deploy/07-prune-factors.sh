#!/usr/bin/env bash
# Delete the factor items (PINs, passkeys) of clients that were soft-deleted.
# Without --apply it only prints counts per client. Safe to rerun.
#
# This needs dynamodb:Scan, which neither Lambda role nor the deployer has, so it runs
# under the operator's profile:
#   ADMIN_PROFILE=default ./07-prune-factors.sh [--apply]
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

flags=()
case "${1:-}" in
  "") ;;
  --apply) flags=(-apply) ;;
  *) die "usage: 07-prune-factors.sh [--apply]" ;;
esac
[ "$#" -le 1 ] || die "usage: 07-prune-factors.sh [--apply]"

export AWS_PROFILE="${ADMIN_PROFILE:-default}" AWS_REGION="$REGION"
cd "$ROOT"
go run ./cmd/prune-factors -table "auth-gate-$STAGE" ${flags[@]+"${flags[@]}"}
