#!/usr/bin/env bash
# Read only: prints every DNS record the Cloudflare zone needs, from SSM, ACM, the stack
# outputs and SES. Nothing is written anywhere.
#
#   ADMIN_PROFILE=default ./dns-records.sh [STAGE]     (STAGE defaults to stg)
#
# A record whose source does not exist yet is printed as `pending (why)`, never guessed. It works
# before the certificate is issued, so it does not use domain_config.
set -euo pipefail
STAGE="${1:-${STAGE:-stg}}"
export STAGE
# shellcheck source=../setup-and-deploy/lib.sh
# shellcheck source-path=SCRIPTDIR
. "$(dirname "${BASH_SOURCE[0]}")/../setup-and-deploy/lib.sh"

profile="${ADMIN_PROFILE:-default}"
aws_() { aws --profile "$profile" "$@"; }
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# param NAME: the SSM value, or nothing when the parameter does not exist; other errors die.
param() {
  local out
  if out="$(aws_ --region "$REGION" ssm get-parameter --name "$PARAM_PREFIX/$1" \
    --query Parameter.Value --output text 2>"$tmp/err")"; then
    printf '%s' "$out"
  elif grep -q ParameterNotFound "$tmp/err"; then
    return 0
  else
    cat "$tmp/err" >&2
    die "reading $PARAM_PREFIX/$1 failed"
  fi
}

domain="$(param domain)"
[ -n "$domain" ] || die "no domain configured; run 00-cert.sh first"
cert_arn="$(param certificate-arn)"

rows="$tmp/rows"
: >"$rows"
# row NAME TYPE CONTENT
row() { printf '%s\t%s\t%s\tDNS only\n' "$1" "$2" "$3" >>"$rows"; }

# Validation CNAME: the apex and the wildcard share one record, so print it once.
if [ -z "$cert_arn" ]; then
  row "_acm-validation" CNAME "pending (run 00-cert.sh)"
else
  recs="$(aws_ --region us-east-1 acm describe-certificate --certificate-arn "$cert_arn" \
    --query 'Certificate.DomainValidationOptions[].[ResourceRecord.Name,ResourceRecord.Value]' \
    --output text)" || die "cannot describe certificate $cert_arn"
  rec="$(grep -v 'None' <<<"$recs" | sort -u | head -n 1 || true)"
  if [ -n "$rec" ]; then
    vname="" vvalue=""
    read -r vname vvalue <<<"$rec"
    vname="${vname%.}"
    vname="${vname%."$domain"}"
    row "$vname" CNAME "${vvalue%.}"
  else
    row "_acm-validation" CNAME "pending (certificate has no validation record yet)"
  fi
fi

# dist LABEL STACK: a CNAME to the stack's DistributionDomain output.
dist() {
  local host
  if ! host="$(aws_ --region "$REGION" cloudformation describe-stacks --stack-name "$2" \
    --query "Stacks[0].Outputs[?OutputKey=='DistributionDomain'].OutputValue" --output text 2>"$tmp/err")"; then
    if grep -q 'does not exist' "$tmp/err"; then
      row "$1" CNAME "pending (stack $2 not deployed)"
      return 0
    fi
    cat "$tmp/err" >&2
    die "describing stack $2 failed"
  fi
  if [ -z "$host" ] || [ "$host" = None ]; then
    row "$1" CNAME "pending (stack $2 has no DistributionDomain output)"
  else
    row "$1" CNAME "$host"
  fi
}
dist api "auth-gate-$STAGE"
dist admin "auth-gate-console-$STAGE"
dist portal "auth-gate-portal-$STAGE"
dist @ "auth-app-$STAGE"

# DKIM: three tokens, once SES has the identity.
tokens=""
if tokens="$(aws_ --region "$REGION" sesv2 get-email-identity --email-identity "$domain" \
  --query DkimAttributes.Tokens --output text 2>"$tmp/err")"; then
  [ "$tokens" != None ] || tokens=""
elif grep -q NotFoundException "$tmp/err"; then
  tokens=""
else
  cat "$tmp/err" >&2
  die "reading SES identity $domain failed"
fi
if [ -n "${tokens//[[:space:]]/}" ]; then
  for t in $tokens; do row "$t._domainkey" CNAME "$t.dkim.amazonses.com"; done
else
  row "*._domainkey" CNAME "pending (run ses-identity.sh)"
fi

row _dmarc TXT "v=DMARC1; p=none; rua=mailto:dmarc@$domain"

echo "# Records for $domain in Cloudflare. Every record is DNS only (grey cloud), never proxied."
{ printf 'name\ttype\tcontent\tnote\n'; cat "$rows"; } | awk -F'\t' '
  { for (i = 1; i <= NF; i++) { c[NR, i] = $i; if (length($i) > w[i]) w[i] = length($i) } n = NR; nf = NF }
  END { for (r = 1; r <= n; r++) { line = ""
    for (i = 1; i <= nf; i++) line = line sprintf("%-" w[i] "s%s", c[r, i], i < nf ? "  " : "")
    print line } }'
