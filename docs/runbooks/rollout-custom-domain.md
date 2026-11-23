# Rollout of the custom domain

From the `feat-custom-domain` worktree, before the merge, with a person at an admin profile. Each
block asks for what it needs and no secret goes on a command line. `AWS_PROFILE=default` below is
the admin profile, as in the other rollouts. Cloudflare steps are clicks, numbered; each one adds
records that `dns-records.sh` prints, so paste its values rather than typing them.

Merge nothing else to `main`, and re-run no `main` deploy, between step 1 and step 7: `main`'s
templates have no `DomainName`, so a deploy from it would drop the aliases and the redirect while
the new clients already carry the new rpIds.

To add a record in Cloudflare (every record in this rollout):

1. Cloudflare dashboard, `authgatehq.com`, DNS, Records, Add record.
2. Choose the Type, type the Name and the Target (or Content) exactly as printed.
3. Set Proxy status to DNS only (grey cloud). Save.

Never query a name before its record exists: a resolver that answered "no such name" keeps that
answer for the zone's negative TTL (1800 s), and a smoke run too early fails with `ENOTFOUND` for
up to 30 minutes. After adding `api`, `admin` or `portal`, check it resolves before its smoke:

```bash
for i in $(seq 10); do getent ahostsv4 api.authgatehq.com >/dev/null && printf 'ok ' || printf 'FAIL '; sleep 1; done; echo
```

**1. The certificate.** Request it, then add the validation CNAME it prints (the apex and the
wildcard share one record) and wait for `ISSUED`. Validation usually takes a few minutes after the
record resolves. Keep this CNAME forever: ACM renews the certificate through it, and without it
all three apps lose HTTPS about 13 months later.

```bash
(
export AWS_REGION=ap-southeast-1 ADMIN_PROFILE=default
cd /home/work/auth-gate/feat-custom-domain
scripts/setup-and-deploy/00-cert.sh authgatehq.com
)
```

Expect `Requested certificate arn:aws:acm:us-east-1:...`, then the record as `name:` and `value:`
lines under `Add this CNAME at the DNS provider (DNS only):`, then
`Wrote /credo/auth-gate/stg/domain and /credo/auth-gate/stg/certificate-arn`. Add the record in
Cloudflare, then:

```bash
(
export AWS_REGION=ap-southeast-1 ADMIN_PROFILE=default
cd /home/work/auth-gate/feat-custom-domain
scripts/setup-and-deploy/00-cert.sh authgatehq.com --wait
)
```

Expect `Reusing certificate ...`, then `Status PENDING_VALIDATION (poll n); ...` lines, ending in
`Certificate is ISSUED`.

**2. The SES identity.** Create it, add the three DKIM CNAMEs and the DMARC record, then rerun the
script until SES verifies the domain.

```bash
(
export AWS_REGION=ap-southeast-1 ADMIN_PROFILE=default
cd /home/work/auth-gate/feat-custom-domain
scripts/domain/ses-identity.sh authgatehq.com
scripts/domain/dns-records.sh
)
```

Expect `Created SES identity authgatehq.com in ap-southeast-1` and `VerifiedForSendingStatus:
False`. The table lists the certificate record, the three `<token>._domainkey` CNAMEs
to `<token>.dkim.amazonses.com`, and `_dmarc` TXT `v=DMARC1; p=none; rua=mailto:dmarc@authgatehq.com`. The `api`, `admin`, `portal`
and `@` rows read `pending (stack ... not deployed)` or show the CloudFront names of stacks that
exist: those come later. Add the three DKIM records and the `_dmarc` record in Cloudflare, wait a
few minutes, then rerun:

```bash
(
cd /home/work/auth-gate/feat-custom-domain
ADMIN_PROFILE=default AWS_REGION=ap-southeast-1 scripts/domain/ses-identity.sh authgatehq.com
)
```

Expect `SES identity authgatehq.com already exists in ap-southeast-1`, `VerifiedForSendingStatus:
True` and `DkimAttributes.Status:     SUCCESS`. The account suppression of bounces and complaints
was set in the [Portal rollout](rollout-p2.md#rollout-of-p2-the-portal); it applies to the domain too.

**3. The deployer policy and the CI role**, back to back, as in the other rollouts (no merge or
deploy between them), then a minute for IAM:

```bash
(
set -euo pipefail
cd /home/work/auth-gate/feat-custom-domain
ADMIN_PROFILE=default scripts/setup-and-deploy/00-deployer.sh
ADMIN_PROFILE=default scripts/ci/00-ci-role.sh
sleep 60
)
```

**4. AuthGate on `api.`** From here to step 6 (and 4b) the apps refuse tokens until each is redeployed:
keep going. Before the Console and Portal steps, add the CNAME `api` printed by `dns-records.sh`
(it needs the stack), then smoke.

```bash
(
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
cd /home/work/auth-gate/feat-custom-domain
scripts/setup-and-deploy/02-build.sh
scripts/setup-and-deploy/03-deploy.sh
ADMIN_PROFILE=default scripts/domain/dns-records.sh
)
```

Expect `BaseUrl: https://api.authgatehq.com` and `RawVerifyUrl: ...`. A CloudFront update takes
several minutes. Add the `api` CNAME in Cloudflare (Target: the distribution name from the `api`
row), check it resolves (the loop above), then:

```bash
cd /home/work/auth-gate/feat-custom-domain && pnpm install --frozen-lockfile && (
AWS_PROFILE=default scripts/setup-and-deploy/04-smoke.sh
)
```

**4b. The auth-app demo, on the new AuthGate address.** auth-app's step-up verifies tokens
against `iss = AUTHGATE_BASE_URL`, so after step 4 the deployed `auth-app-stg` refuses every
token. Redeploy it from its own worktree with only the new base URL: the same client and the same
CloudFront host (AuthGate answers on both addresses; plan B moves the rest). `AUTHGATE_CLIENT_ID`
stays unset, so `sam` keeps the deployed client id. auth-app's scripts default to the
`auth-app-deployer` profile, region `ap-southeast-1` and stage `stg`, so set none of them. Nothing
in auth-app's CI deploys, so nothing reverts this.

```bash
cd /home/work/auth-app/main && pnpm install --frozen-lockfile && (
set -euo pipefail
export AUTHGATE_BASE_URL=https://api.authgatehq.com
scripts/setup-and-deploy/02-build.sh
scripts/setup-and-deploy/03-deploy.sh
scripts/setup-and-deploy/04-smoke.sh
)
```

Expect `built bff/dist and app/dist`, the `sam` changeset, `invalidated <id>`, `BaseUrl: https://...cloudfront.net`
and `RawBffUrl: ...`, then the Bruno run and `smoke ok`. `03-deploy.sh` also prints
`AUTHGATE_CLIENT_ID is empty: /api returns 502 until the second pass`; ignore it here, the stack
keeps its client id.

**5. The Console on `admin.`** Deploy on the new name (the old client id stays for now), register
the new client (type the confirmation it asks for), deploy again with the new id. Add the CNAME
`admin`, smoke, then sign in and enrol.

```bash
cd /home/work/auth-gate/feat-custom-domain && pnpm install --frozen-lockfile && (
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
scripts/console/deploy.sh                          # alias, AuthGateBaseUrl on api.; old id kept
scripts/console/register-client.sh stg --replace   # asks: Type 'replace auth-gate-console-stg' ...
read -r -p 'AUTHGATE_CLIENT_ID printed above: ' AUTHGATE_CLIENT_ID
export AUTHGATE_CLIENT_ID
scripts/console/deploy.sh                          # sets the new client id
ADMIN_PROFILE=default scripts/domain/dns-records.sh
)
```

Expect `BaseUrl: https://admin.authgatehq.com`; from `register-client.sh`,
`registered auth-gate-console-stg; wrote /credo/auth-gate-console/stg/authgate-client-key
(SecureString)`, `disable the old client <old id> in the Console once the new one works` (note the
old id) and `AUTHGATE_CLIENT_ID=<new id>`. Add the `admin` CNAME in Cloudflare (Target: the
`admin` row), check it resolves (the loop above, with `admin`), then:

```bash
cd /home/work/auth-gate/feat-custom-domain && pnpm install --frozen-lockfile && (
AWS_PROFILE=default scripts/console/smoke.sh
)
```

Then open `https://admin.authgatehq.com`, sign in with your password, and add a PIN or a passkey
at once. Your factors under the old client are gone.

**6. The Portal on `portal.`** First, in the Cloudflare dashboard, add the new host to the
Turnstile widget:

1. Cloudflare dashboard, Turnstile, the widget `auth-gate-portal-stg`.
2. Settings, Hostname management: add `portal.authgatehq.com` next to the existing hostnames.
3. Save. Keep the old CloudFront host for now.

Then deploy and register, deploy again, and (after the smoke) point `PORTAL_EMAIL_FROM` at the new sender. The Turnstile
secret is already in SSM and is kept.

```bash
cd /home/work/auth-gate/feat-custom-domain && pnpm install --frozen-lockfile && (
set -euo pipefail
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
export EMAIL_FROM=no-reply@authgatehq.com
TURNSTILE_SITE_KEY="$(aws cloudformation describe-stacks --stack-name auth-gate-portal-stg \
  --query "Stacks[0].Parameters[?ParameterKey=='TurnstileSiteKey'].ParameterValue" --output text)"
[[ -n "$TURNSTILE_SITE_KEY" && "$TURNSTILE_SITE_KEY" != None ]] || { echo "no TurnstileSiteKey on the stack" >&2; exit 1; }
echo "site key: $TURNSTILE_SITE_KEY"
export TURNSTILE_SITE_KEY
scripts/portal/deploy.sh                           # alias, PublicBaseUrl on portal.; old id kept
scripts/portal/register-client.sh stg --replace    # asks: Type 'replace auth-gate-portal-stg' ...
read -r -p 'AUTHGATE_CLIENT_ID printed above: ' AUTHGATE_CLIENT_ID
export AUTHGATE_CLIENT_ID
scripts/portal/deploy.sh                           # sets the new client id
ADMIN_PROFILE=default scripts/domain/dns-records.sh
)
```

The site key (public) is read from the deployed stack's `TurnstileSiteKey` parameter, so there
is no prompt and no `gh` is needed. Expect `site key: 0x...`, then
`BaseUrl: https://portal.authgatehq.com` and `AUTHGATE_CLIENT_ID=<new id>`; write down the old id
the register step printed. Add the `portal` CNAME in Cloudflare, check it resolves, then smoke:

```bash
cd /home/work/auth-gate/feat-custom-domain && pnpm install --frozen-lockfile && (
AWS_PROFILE=default scripts/portal/smoke.sh
)
```

Once the smoke passes, change the repository variable `PORTAL_EMAIL_FROM` to
`no-reply@authgatehq.com`: GitHub, the repository, Settings, Secrets and variables, Actions,
Variables, `PORTAL_EMAIL_FROM`, edit, Update. (With the `gh` CLI:
`gh variable set PORTAL_EMAIL_FROM --body no-reply@authgatehq.com`.) Then sign up in a browser:

1. Open `https://portal.authgatehq.com` and sign up. While SES is in its sandbox the address must
   be one SES has verified (the old sender address will do); a stranger's address works only after
   step 9.
2. The verification email arrives from `AuthGate <no-reply@authgatehq.com>`. In your mail client,
   show the original message: DKIM is `pass` for `authgatehq.com`.
3. Open the link, land in `enroll`, add a PIN or a passkey, and save the recovery codes.
4. An account that existed under the old client enrols a factor again the same way.

**If a line in a step 5 or 6 block fails,** resume from that line; do not rerun the block.
Rerunning `--replace` registers a second new client and orphans the first.

**If a step fails.** The default is to fix forward. To send one stack back to its CloudFront
address, an admin runs the block below (set `STACK` to `auth-gate-stg`, `auth-gate-console-stg` or
`auth-gate-portal-stg`). It keeps every parameter at its previous value except `DomainName` and
`CertificateArn`, which become empty, and waits for the update.

```bash
(
set -euo pipefail
export ADMIN_PROFILE=default STACK=auth-gate-console-stg
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
dd="$(aws cloudformation describe-stacks --stack-name "$STACK" --profile "$ADMIN_PROFILE" --region ap-southeast-1 \
  --query "Stacks[0].Outputs[?OutputKey=='DistributionDomain'].OutputValue" --output text)"
[[ "$dd" == *.cloudfront.net ]] || { echo "no DistributionDomain output on $STACK" >&2; exit 1; }
aws cloudformation describe-stacks --stack-name "$STACK" --profile "$ADMIN_PROFILE" --region ap-southeast-1 \
  --query 'Stacks[0].Parameters[].ParameterKey' --output json |
  jq --arg url "https://$dd" 'map(if . == "DomainName" or . == "CertificateArn"
          then {ParameterKey: ., ParameterValue: ""}
          elif . == "PublicBaseUrl" then {ParameterKey: ., ParameterValue: $url}
          else {ParameterKey: ., UsePreviousValue: true} end)' >"$tmp/params.json"
aws cloudformation update-stack --stack-name "$STACK" --use-previous-template \
  --parameters "file://$tmp/params.json" \
  --capabilities CAPABILITY_IAM CAPABILITY_NAMED_IAM CAPABILITY_AUTO_EXPAND \
  --profile "$ADMIN_PROFILE" --region ap-southeast-1
aws cloudformation wait stack-update-complete --stack-name "$STACK" \
  --profile "$ADMIN_PROFILE" --region ap-southeast-1
)
```

This works for AuthGate at any point, and for the Console or the Portal only before their
`--replace`. After `--replace` their new client carries the new rpId, so they fix forward. The
capabilities are listed because the templates are SAM transforms and may name IAM resources. The
Portal's `PublicBaseUrl` (email links, the avatar bucket's CORS origin) is set back to the
distribution's own address too.

Sending AuthGate back moves the token issuer back to its CloudFront address. Then redeploy the
Console and the Portal (their deploy scripts read AuthGate's `BaseUrl` output) and auth-app
(`AUTHGATE_BASE_URL` set to that address), or they refuse its tokens. Before the merge, `main`'s CI
does not read the SSM parameters; after it, also delete `/credo/auth-gate/stg/domain`, or the next
CI deploy moves the stack forward again.

**7. Merge.** Push the branch, open the pull request and merge it. CI runs `check`, then redeploys
AuthGate, the Console and the Portal from the SSM parameters (`domain`, `certificate-arn`), with
`PORTAL_EMAIL_FROM` from step 6, and runs the three smokes.

**8. auth-app.** Its own plan (plan B), in its own repository, after this is live: it takes the apex
and its own client. Step 4b already keeps the demo working until then. The apex `@` row of `dns-records.sh` stays `pending` until its stack exists.

**9. SES production access.** In the sandbox SES sends only to verified addresses. Request
production access in the AWS console:

1. AWS console, region Asia Pacific (Singapore) `ap-southeast-1`, Amazon SES.
2. Get set up (the Account dashboard's sandbox banner links to it), Request production access.
3. Mail type: Transactional. Website URL: `https://portal.authgatehq.com`.
4. Paste the text below into "Use case description". Acknowledge the terms and submit.

```text
AuthGate is a two-factor verification service (PIN and passkeys). Its Portal, at
https://portal.authgatehq.com, lets developers sign up and manage the clients they register with the
service. We use SES only for transactional email sent from no-reply@authgatehq.com, a verified
domain with DKIM and a DMARC record: the account verification link, the password reset link, and
security notices (confirming a reset of sign-in factors or a change of email address, and a notice
to an address that already has an account). We send only to people who signed up for an account
on the Portal themselves and must prove they own the address before anything else is sent. We send
no marketing, newsletters or purchased lists.

Volume: under 100 emails a day, in the low tens in practice.

Bounces and complaints: the account-level suppression list is on for BOUNCE and COMPLAINT, so SES
stops mailing any address that bounced or complained. Sign-up, resending the verification link, and
the password and factor reset requests are protected by Cloudflare Turnstile and by per-address and
per-IP rate limits, so the service cannot be used to send mail to people who did not ask for it.
Moving an account to a new email address requires the account's second factor first. Mail is sent
from a no-reply address; replies go to support@authgatehq.com, which a person reads.
```

This is the text sent on 2026-11-23; AWS granted production access the same day. Until approval,
verified addresses keep working. Check it with
`aws sesv2 get-account --profile default --region ap-southeast-1 --query ProductionAccessEnabled`
(`True` once approved).

**10. Clean-up.**

1. Disable the old clients at once. In the Console, Clients, open the client whose id the register
   steps printed (`auth-gate-console-stg` and `auth-gate-portal-stg` have the same name as the new
   ones; the id tells them apart), then Availability, Disable client. Do the same for the old
   Portal client.
2. About a week later, if nothing broke, open each of them again and Delete client, and confirm.
   Then clear their factors: `ADMIN_PROFILE=default scripts/setup-and-deploy/07-prune-factors.sh`
   (counts only), then the same with `--apply`.
3. Remove the old CloudFront host from the Turnstile widget (Turnstile, `auth-gate-portal-stg`,
   Settings, Hostname management).
4. Delete the old Gmail identity from SES only after step 9 shows `ProductionAccessEnabled`
   `True`: while SES is in its sandbox that identity is the only verified recipient.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1
read -r -p 'Old sender address to delete: ' addr
aws sesv2 delete-email-identity --email-identity "$addr"
)
```

5. After a few weeks of clean sending, change the `_dmarc` TXT record in Cloudflare from `p=none`
   to `v=DMARC1; p=quarantine; rua=mailto:dmarc@authgatehq.com`.

**The proofs**, read-only, under the admin profile in a subshell. Expect: `301` and the new
hostname with the path and query kept, for the Console's and the Portal's old address (for
example `301 https://portal.authgatehq.com/sign-in?a=1&a=2&q=a%20b`, the `q` value unchanged); then `True` and `SUCCESS` for the SES
identity. The smokes of steps 4 to 6 already proved the three new names.

```bash
(
export AWS_PROFILE=default AWS_REGION=ap-southeast-1 STAGE=stg
out() { aws cloudformation describe-stacks --stack-name "$1" \
  --query "Stacks[0].Outputs[?OutputKey=='$2'].OutputValue | [0]" --output text; }
for s in "auth-gate-console-$STAGE" "auth-gate-portal-$STAGE"; do
  curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "https://$(out "$s" DistributionDomain)/sign-in?a=1&a=2&q=a%20b"
done
aws sesv2 get-email-identity --email-identity authgatehq.com \
  --query '[VerifiedForSendingStatus,DkimAttributes.Status]' --output text
)
```
