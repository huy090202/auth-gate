#!/usr/bin/env bash
# Write the list-index item for every client created before the index existed.
# Safe to rerun: an existing index item is never overwritten. Prints counts only.
#
# This needs dynamodb:Scan, which neither Lambda role nor the deployer has (adding
# it would widen them for a one-off), so it runs under the operator's profile:
#   ADMIN_PROFILE=default ./05-backfill-clients.sh
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

export AWS_PROFILE="${ADMIN_PROFILE:-default}" AWS_REGION="$REGION"
cd "$ROOT"
go run ./cmd/backfill-clients -table "auth-gate-$STAGE"
