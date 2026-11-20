#!/usr/bin/env bash
# Create the first master: create-admin.sh --username U. See admin.sh.
set -euo pipefail
exec "$(dirname "${BASH_SOURCE[0]}")/admin.sh" create --role master "$@"
