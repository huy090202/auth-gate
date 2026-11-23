# Custom domain

Every app has a name on `authgatehq.com` (the decision and its reasons:
[custom-domain](../memo/custom-domain.md); the design:
[`docs/superpowers/specs/2026-11-23-custom-domain-design.md`](../superpowers/specs/2026-11-23-custom-domain-design.md)).

| App | Hostname | rpId |
| --- | --- | --- |
| auth-app (docs and demo; its own repository) | `authgatehq.com` | `authgatehq.com` |
| Portal | `portal.authgatehq.com` | `portal.authgatehq.com` |
| Console | `admin.authgatehq.com` | `admin.authgatehq.com` |
| AuthGate API | `api.authgatehq.com` | none |

The hostnames are fixed in the scripts as prefixes of the `domain` parameter. DNS is at Cloudflare
and is yours to create: no script writes it and no API token exists. Every record is **DNS only**
(grey cloud, never proxied): a proxy would hide the viewer's IP from the per-IP rate limits and
from the audit log.

The scripts, all run by a person under an admin profile (`ADMIN_PROFILE`, default `default`):

- `scripts/setup-and-deploy/00-cert.sh DOMAIN [--wait]`: one ACM certificate in `us-east-1` for
  `DOMAIN` and `*.DOMAIN`, DNS validation. Rerunning reuses an issued or pending one. It writes
  `/credo/auth-gate/<stage>/domain` and `/credo/auth-gate/<stage>/certificate-arn`, prints the
  validation CNAME, and with `--wait` polls until the certificate is `ISSUED`.
- `scripts/domain/ses-identity.sh DOMAIN`: the SES domain identity with Easy DKIM (RSA 2048) in
  `ap-southeast-1`. Safe to rerun; it prints the verification status.
- `scripts/domain/dns-records.sh [STAGE]`: read only. Prints every record the Cloudflare zone
  needs, each marked DNS only. A record whose source does not exist yet is `pending (why)`, never
  a guess. Rerun it at each step of the
  [rollout](../runbooks/rollout-custom-domain.md).
- `register-client.sh stg --replace` (the Console's and the Portal's): register a new client with
  the new rpId, overwrite the stored client key only after you type the confirmation it asks for,
  and print the new id. It refuses while the stack's `BaseUrl` is still the CloudFront address.

A stack that was never deployed with a domain is unchanged: with no `domain` parameter its deploy
uses the CloudFront address. The move is **forward-only per stack**: once a stack has been deployed
with `DomainName` and `CertificateArn`, deleting the SSM parameter does not move it back, because
`sam deploy` reuses the previous values (see "If a step fails"). With a domain, a deploy stops
before `sam deploy` unless the certificate is `ISSUED`. CI passes no client id, and an
empty id keeps the deployed one, so after the rollout CI keeps the new clients and needs no
repository variable for them. Only `PORTAL_EMAIL_FROM` changes.

The Console's and the Portal's old CloudFront address answers `301` to the new hostname, keeping
the path and query, because a passkey made on the new origin fails on the old one. AuthGate's
distribution does not redirect and answers on both addresses.

## What this costs

- AuthGate refuses an rpId change, so the Console and the Portal get **new clients**. Every PIN and
  passkey under the old clients is gone: after each Console or Portal replace, sign in at once
  and enrol a PIN or a passkey. Until you do, whoever holds the account's password can enrol its
  first factor. Portal users and auth-app demo users enrol again too. Tenant clients are unaffected.
- The token issuer changes to `https://api.authgatehq.com` when AuthGate is deployed on its name.
  The Console and the Portal read AuthGate's `BaseUrl` output in their deploy scripts, and the
  auth-app demo takes it as `AUTHGATE_BASE_URL`; each refuses the new tokens until it is
  redeployed. Do steps 4 to 6, including 4b, back to back. On stg a short sign-in outage is
  accepted. Tokens issued before live at most 15 minutes.

## Mail to the domain

Until now no address at `authgatehq.com` received mail. The Portal's emails now carry
`Reply-To: support@authgatehq.com`, and Cloudflare Email Routing (free) forwards four addresses to
your own inbox. The design:
[`docs/superpowers/specs/2026-11-23-portal-email-design.md`](../superpowers/specs/2026-11-23-portal-email-design.md).

**Email Routing, once, in the Cloudflare dashboard.**

1. Cloudflare dashboard, the zone `authgatehq.com`, Email, Email Routing, Enable. Accept the
   records it offers to add (MX, an SPF TXT at the apex, and its own DKIM TXT).
2. Destination addresses, add your inbox, then open the verification mail Cloudflare sends there
   and confirm it.
3. Routing rules, Create address, Custom address: one rule each for `support`, `dmarc`,
   `postmaster` and `abuse`. Action Send to an email, destination the verified inbox. Save.
4. Leave the catch-all disabled.

**SES is unaffected.** SES mail has the envelope sender `amazonses.com`, so the apex SPF that
Cloudflare adds does not apply to it, and DMARC keeps passing on DKIM. Cloudflare manages the MX,
SPF and DKIM records it adds, so `dns-records.sh` does not print them.

**`_dmarc` gets a report address.** `dns-records.sh` now prints
`v=DMARC1; p=none; rua=mailto:dmarc@authgatehq.com`. In Cloudflare, edit the existing `_dmarc` TXT
record to that value (still DNS only). It is an edit of a name that already resolves, so no wait
is needed. `dmarc@` is one of the four routed addresses, so the reports reach your inbox.

**`SUPPORT_EMAIL`.** The Portal's configuration is `SUPPORT_EMAIL` (template parameter
`SupportEmail`), optional. `scripts/portal/deploy.sh` sets it to `support@<domain>` when a domain
is configured and passes nothing otherwise, so a stage without a domain sends no Reply-To and the
notice footer says "contact the operator who runs this service". CI needs no new variable: the
domain already comes from SSM.

**Rollout.**

1. Email Routing, as above. Send a test mail from another address to `support@authgatehq.com`:
   it reaches your inbox.
2. Edit `_dmarc` in Cloudflare, then print what the zone should hold and compare:

```bash
(
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
cd /home/work/auth-gate/feat-portal-email
ADMIN_PROFILE=default scripts/domain/dns-records.sh | grep '^_dmarc'
)
```

   Expect one row, its columns space-padded: `_dmarc`, `TXT`,
   `v=DMARC1; p=none; rua=mailto:dmarc@authgatehq.com`, `DNS only`.
3. Deploy the Portal from the branch. The site key is read from the stack, as in step 6 of the
   [custom-domain rollout](../runbooks/rollout-custom-domain.md):

```bash
cd /home/work/auth-gate/feat-portal-email && pnpm install --frozen-lockfile && (
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
export EMAIL_FROM=no-reply@authgatehq.com
TURNSTILE_SITE_KEY="$(aws cloudformation describe-stacks --stack-name auth-gate-portal-stg \
  --query "Stacks[0].Parameters[?ParameterKey=='TurnstileSiteKey'].ParameterValue" --output text)"
[[ -n "$TURNSTILE_SITE_KEY" && "$TURNSTILE_SITE_KEY" != None ]] || { echo "no TurnstileSiteKey on the stack" >&2; exit 1; }
echo "site key: $TURNSTILE_SITE_KEY"
export TURNSTILE_SITE_KEY
scripts/portal/deploy.sh
)
```

   Then smoke it:

```bash
cd /home/work/auth-gate/feat-portal-email && pnpm install --frozen-lockfile && (
AWS_PROFILE=default scripts/portal/smoke.sh
)
```

4. At `https://portal.authgatehq.com`, trigger each email the browser can: sign up, reset a
   password, add a factor. Open each in Gmail and in one other client. Check the layout, the
   button and the fallback link, then "Show original": SPF, DKIM and DMARC are `PASS`. Reply to
   one: the reply reaches your inbox through `support@`.
5. Merge. CI redeploys the Portal with `SupportEmail` from the SSM domain.
