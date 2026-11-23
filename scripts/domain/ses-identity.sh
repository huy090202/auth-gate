#!/usr/bin/env bash
# Admin: a verified SES domain identity with Easy DKIM (RSA 2048) in the app's region, where the
# Portal sends. Safe to rerun.
#
#   ADMIN_PROFILE=default ./ses-identity.sh DOMAIN
#
# Creates the identity (an existing one is kept), then prints its verification and DKIM status.
# dns-records.sh prints the three DKIM CNAMEs; SES verifies the domain once they resolve.
set -euo pipefail
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

[ $# -eq 1 ] && [[ "$1" != -* ]] || die "usage: ses-identity.sh DOMAIN"
domain="$1"

profile="${ADMIN_PROFILE:-default}"
ses() { aws --profile "$profile" --region "$REGION" sesv2 "$@"; }

err="$(mktemp)"
trap 'rm -f "$err"' EXIT
if ses create-email-identity --email-identity "$domain" \
  --dkim-signing-attributes NextSigningKeyLength=RSA_2048_BIT >/dev/null 2>"$err"; then
  echo "Created SES identity $domain in $REGION"
elif grep -q AlreadyExistsException "$err"; then
  echo "SES identity $domain already exists in $REGION"
else
  cat "$err" >&2
  die "creating SES identity $domain failed"
fi

ses get-email-identity --email-identity "$domain" \
  --query '[VerifiedForSendingStatus,DkimAttributes.Status]' --output text |
  awk '{print "VerifiedForSendingStatus: " $1; print "DkimAttributes.Status:     " $2}'
