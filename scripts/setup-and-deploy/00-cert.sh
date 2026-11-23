#!/usr/bin/env bash
# Admin, run once: one ACM certificate in us-east-1 (CloudFront needs it there) for the
# domain and its wildcard, DNS validation, and the two SSM parameters every deploy reads.
#
#   ADMIN_PROFILE=default ./00-cert.sh DOMAIN [--wait]
#
# A certificate for DOMAIN that is ISSUED or PENDING_VALIDATION is reused; any other is
# ignored and a new one requested. Prints the validation CNAME (the wildcard shares the
# apex's record) and the ARN, and writes /credo/auth-gate/$STAGE/domain and
# /credo/auth-gate/$STAGE/certificate-arn (String, overwritten). With --wait, polls every
# 15 s for up to 30 minutes until the certificate is ISSUED; dies if it FAILED.
set -euo pipefail
# shellcheck source=lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

POLL_SECONDS="${POLL_SECONDS:-15}"
MAX_POLLS="${MAX_POLLS:-120}"

domain=""
wait=0
for arg in "$@"; do
  case "$arg" in
    --wait) wait=1 ;;
    -*) die "usage: 00-cert.sh DOMAIN [--wait]" ;;
    *) [ -z "$domain" ] || die "usage: 00-cert.sh DOMAIN [--wait]"; domain="$arg" ;;
  esac
done
[ -n "$domain" ] || die "usage: 00-cert.sh DOMAIN [--wait]"

profile="${ADMIN_PROFILE:-default}"
acm() { aws --profile "$profile" --region us-east-1 acm "$@"; }

# Reuse: the first ISSUED certificate for the domain, else the first pending one.
listing="$(acm list-certificates --certificate-statuses ISSUED PENDING_VALIDATION \
  --query "CertificateSummaryList[?DomainName=='$domain'].[CertificateArn,Status]" --output text)"
arn="$(awk '$2 == "ISSUED" {print $1; exit}' <<<"$listing")"
[ -n "$arn" ] || arn="$(awk 'NF >= 2 {print $1; exit}' <<<"$listing")"

if [ -n "$arn" ]; then
  echo "Reusing certificate $arn"
else
  arn="$(acm request-certificate --domain-name "$domain" \
    --subject-alternative-names "*.$domain" --validation-method DNS \
    --tags Key=app,Value=auth-gate --query CertificateArn --output text)"
  echo "Requested certificate $arn"
fi

# The validation record appears a few seconds after a request.
name="" value=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  rec="$(acm describe-certificate --certificate-arn "$arn" \
    --query 'Certificate.DomainValidationOptions[0].ResourceRecord.[Name,Value]' --output text)"
  if [ -n "$rec" ] && [ "$rec" != None ]; then
    read -r name value <<<"$rec"
    break
  fi
  sleep "$POLL_SECONDS"
done
if [ -n "$name" ] && [ -n "$value" ]; then
  echo "Add this CNAME at the DNS provider (DNS only):"
  echo "  name:  $name"
  echo "  value: $value"
else
  echo "warning: the validation record of $arn is not available yet; rerun, or check the ACM console" >&2
fi
echo "Certificate ARN: $arn"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
put() {
  printf '{"Name":"%s/%s","Value":"%s","Type":"String","Overwrite":true}\n' "$PARAM_PREFIX" "$1" "$2" >"$tmp/p.json"
  aws --profile "$profile" --region "$REGION" ssm put-parameter --cli-input-json "file://$tmp/p.json" >/dev/null
}
put domain "$domain"
put certificate-arn "$arn"
echo "Wrote $PARAM_PREFIX/domain and $PARAM_PREFIX/certificate-arn"

if [ "$wait" -eq 1 ]; then
  for ((i = 1; i <= MAX_POLLS; i++)); do
    status="$(acm describe-certificate --certificate-arn "$arn" --query Certificate.Status --output text)"
    case "$status" in
      ISSUED) echo "Certificate is ISSUED"; exit 0 ;;
      FAILED) die "certificate $arn FAILED validation" ;;
    esac
    echo "Status $status (poll $i); waiting for the validation CNAME to resolve..."
    sleep "$POLL_SECONDS"
  done
  die "certificate $arn not ISSUED after $((MAX_POLLS * POLL_SECONDS)) s"
fi
