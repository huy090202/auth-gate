# Custom domain: authgatehq.com for every app, and email from the domain

Date: 2026-11-23. Status: approved by the product owner in chat, section by section, the same day.

## 1. Purpose

Every app runs on a `*.cloudfront.net` address today and the Portal mails from a personal Gmail
address inside the SES sandbox, so strangers cannot receive its emails. The owner bought
`authgatehq.com` at Namecheap and moved its nameservers to Cloudflare (Free plan, zone active).
This change puts every app on that domain, sends email from it, and takes SES out of the sandbox.

Decided by the product owner (not re-askable):

- The tidy names point at the only stage there is, stg. If production ever exists it takes these
  names and stg moves to `*.stg.authgatehq.com`.
- auth-app is included, with its own plan in its own repository.
- SES production access is requested in this work.
- One certificate, DNS records added by hand once (no Cloudflare API token anywhere).

Constraints carried from earlier decisions: DNS stays at Cloudflare and every record is DNS only
(a proxy would hide the viewer IP from the per-IP limits and the audit log); no fixed monthly cost.

**Done when:** all four apps answer over HTTPS on their names; a passkey sign-in works on each
new origin; a stranger's address can sign up on the Portal and receives the verification email
from `no-reply@authgatehq.com`, signed by the domain; CI deploys green.

## 2. Names

| App | Hostname | rpId |
|---|---|---|
| auth-app (docs and demo) | `authgatehq.com` | `authgatehq.com` |
| Portal | `portal.authgatehq.com` | `portal.authgatehq.com` |
| Console | `admin.authgatehq.com` | `admin.authgatehq.com` |
| AuthGate API | `api.authgatehq.com` | — |

Each app keeps its own rpId, so the operator's, the customers' and the demo's passkeys never mix.

## 3. Certificate, configuration and DNS

- `scripts/setup-and-deploy/00-cert.sh` is rewritten. Run once by an admin, it requests one ACM
  certificate in `us-east-1` for `authgatehq.com` with the SAN `*.authgatehq.com`, DNS
  validation, tag `app=auth-gate`. Rerunning reuses a certificate for that domain that is issued
  or pending. `--wait` polls until it is `ISSUED`. It writes two String parameters:
  `/credo/auth-gate/<stage>/domain` (`authgatehq.com`) and
  `/credo/auth-gate/<stage>/certificate-arn`.
- Deploy scripts read both parameters. When `domain` is absent every app deploys as today, on its
  CloudFront address; other stages and local development are unaffected. The hostnames of §2 are
  fixed in the scripts as prefixes of `domain`.
- Route 53 leaves: `infra/template.yaml` loses `HostedZoneId` and its `AWS::Route53::RecordSet`,
  and `infra/deployer-policy-edge.json` loses its Route 53 actions. The ACM read and request rights
  stay.
- `scripts/domain/dns-records.sh`, read only, prints every record the Cloudflare zone needs, from
  ACM, the stack outputs and SES: the certificate's validation CNAME; CNAMEs `api`, `admin`,
  `portal` and `@` to their distributions (Cloudflare flattens the apex CNAME, DNS only included);
  the three SES DKIM CNAMEs; `_dmarc` TXT. Each record says DNS only. A record whose source does not
  exist yet (a stack not deployed) is printed as pending, not guessed.
- Old addresses: a CloudFront Function on the Console, Portal and auth-app distributions answers
  `301` to `https://<the app's hostname><path and query>` when the `Host` header is not the app's
  hostname, because a passkey made on the new origin fails on the old one. AuthGate's API keeps
  answering on both addresses during the move.
- Turnstile: `portal.authgatehq.com` joins the widget's hostnames; the old CloudFront host leaves
  after the move.

## 4. The apps

**AuthGate.** With `domain` set, `03-deploy.sh` passes `DomainName=api.<domain>` and the
certificate ARN; `BASE_URL`, and so the JWT issuer, becomes `https://api.authgatehq.com`. Tokens
issued before live at most 15 minutes and are refused by apps already on the new issuer; their
users sign in again. Without a domain the two-pass `DistributionBaseUrl` path stays.

**Console.** `infra/console.yaml` gains `DomainName` and `CertificateArn` (aliases, viewer
certificate, `BaseUrl` output on the domain) and the redirect in its existing `SpaRewrite`
function. `AuthGateBaseUrl` becomes `https://api.<domain>`.

**Portal.** The same as the Console, plus `PublicBaseUrl = https://portal.<domain>` (email links
and the avatar bucket's CORS origin) and `EMAIL_FROM = no-reply@<domain>`; the repository variable
`PORTAL_EMAIL_FROM` changes with it. The site CSP pin is unchanged.

**auth-app** (its own repository and plan). Its template gains the apex domain, aliases,
certificate and redirect; `AuthGateBaseUrl` becomes `https://api.<domain>`; its own deploy reads
its own parameters `/credo/auth-app/<stage>/domain` and `/certificate-arn` (the same certificate
ARN), so its deployer needs no right on auth-gate's parameters. No new features: the "Get a key"
page stays in P4.

Smoke scripts and Bruno read `BaseUrl` from stack outputs, so they run on the domain unchanged. CI
needs no new variable.

## 5. Email

- `scripts/domain/ses-identity.sh`, run once by an admin, creates the SES domain identity
  `authgatehq.com` with Easy DKIM (RSA 2048), safe to rerun; `dns-records.sh` prints its three
  CNAMEs. Once they resolve, SES verifies the domain and any `@authgatehq.com` sender works.
- The Portal sends as `AuthGate <no-reply@authgatehq.com>`: the display name is added in code; the
  IAM condition `ses:FromAddress` keeps matching the bare address.
- No custom MAIL FROM: DKIM on the domain makes DMARC pass. DMARC starts at `p=none` and moves to
  `p=quarantine` after a few weeks of clean sending. Its `rua` address was added later; see
  `2026-11-23-portal-email-design.md`.
- Mail is sent from a no-reply address, and replies go to `support@authgatehq.com`, which a person
  reads (Email Routing, see `2026-11-23-portal-email-design.md`).
- SES production access: the owner submits Request production access in the SES console, region
  `ap-southeast-1`. The README gives the text: transactional mail only (account verification,
  password reset, security notices) to people who signed up and verified themselves; website
  `https://portal.authgatehq.com`; under 100 emails a day; bounces and complaints suppressed at the
  account level, plus Turnstile and rate limits on every sign-up route. Until approval, verified
  addresses keep working.
- The Gmail identity is deleted after the move.

## 6. New clients for the new rpIds

AuthGate refuses an rpId change (passkeys are bound to it), so the Console, the Portal and
auth-app each register a new client.

- `scripts/console/register-client.sh stg --replace` and the Portal's equivalent register a client
  with the rpId and origin of §2, overwrite the stored client key only after a typed confirmation,
  and print the new id for the next deploy. Without `--replace` they still refuse to overwrite.
- After the move the old clients are disabled at once and deleted about a week later (no longer
  the Console's own client, so the Console can delete them).
- Every PIN and passkey under the old clients is gone: Console admins, Portal users and auth-app
  demo users enrol again. Until they do, whoever holds an account's password can enrol its first
  factor. On stg the operator signs in right after each move to enrol. Tenant clients are
  unaffected.
- Tokens now carry the new client ids as audience; each app reads its id from deploy
  configuration. AuthGate's `cmd/smoke` uses its own `smoke.local` rpId and is unaffected.

## 7. Delivery

One spec; two plans:

- **Plan A, auth-gate:** §3, §4 for AuthGate, Console and Portal, §5, §6, docs.
- **Plan B, auth-app:** §4 for auth-app and its client; after Plan A is live.

### Rollout (Plan A, from its worktree, before the merge)

1. `00-cert.sh`, add the validation CNAME at Cloudflare, `00-cert.sh --wait`.
2. `ses-identity.sh`, add the DKIM CNAMEs and `_dmarc`, wait for verification.
3. `00-deployer.sh` then `00-ci-role.sh`, back to back.
4. AuthGate deploy; CNAME `api`; smoke.
4b. auth-app-stg redeployed from its own worktree with only the new `AUTHGATE_BASE_URL`
   (same client, same CloudFront host), so its step-up keeps verifying the `api.` issuer until
   plan B.
5. Console deploy; `register-client.sh stg --replace`; deploy with the new id; CNAME `admin`;
   smoke; sign in and enrol a factor at once.
6. Portal likewise; Turnstile hostname; repository variable `PORTAL_EMAIL_FROM`; CNAME `portal`;
   smoke; a browser sign-up.
7. Merge; CI redeploys all three from the parameters.
8. Plan B.
9. Request SES production access.
10. Clean-up: disable then delete old clients; remove the old Turnstile host; delete the Gmail
    identity; raise DMARC later.

`dns-records.sh` prints, at each step, the records that step needs.

Steps 4 to 6, including 4b, run back to back: from step 4 every consumer of the issuer refuses tokens until it is redeployed.

### Tests

- Stub-`aws` tests: `00-cert.sh` reuses an existing certificate; `register-client.sh --replace`
  overwrites nothing without the typed confirmation; `dns-records.sh` prints the right name, type
  and content per app and marks missing sources as pending.
- The redirect function, run under node: a `*.cloudfront.net` host gets `301` to the hostname with
  path and query kept; the right host passes through to the existing rewrite.
- `sam validate --lint` on every template; the Portal CSP pin test still passes.
- Smoke after every rollout step.

### Docs

`docs/memo/custom-domain.md` (one certificate; DNS only and why; one rpId per app; new clients
rather than an rpId change; DKIM and DMARC); README "Custom domain" with the rollout and the SES
production-access text.

## 8. Non-goals

A production stage; keeping PINs across an rpId change (an operator-only rpId change in AuthGate
would need its own design); custom MAIL FROM; Cloudflare proxy or
WAF; HSTS preload; Route 53.

Inbound email and DMARC reports were non-goals here; `2026-11-23-portal-email-design.md` now covers
them.

Cost: no fixed monthly cost; the domain renews at $15.98 a year.
