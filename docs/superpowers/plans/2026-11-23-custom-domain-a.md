# Custom domain, plan A (auth-gate) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AuthGate, the Console and the Portal deploy on `api.`, `admin.` and `portal.authgatehq.com` with one ACM certificate, redirect their old CloudFront hosts, mail as `AuthGate <no-reply@authgatehq.com>` from a DKIM-signed SES domain identity, and can swap to new AuthGate clients for the new rpIds; scripts print every Cloudflare record needed.

**Architecture:** Admin scripts create the certificate and the SES identity once and store `domain` and `certificate-arn` in SSM. Every deploy script reads them through one shared helper; with no `domain` everything behaves as today. Templates gain aliases and a viewer certificate; the SPA rewrite functions gain a host check.

**Tech Stack:** bash, AWS CLI v2 (acm in us-east-1, sesv2, ssm, cloudformation), SAM/CloudFormation, CloudFront Functions (cloudfront-js-2.0), Go (mail sender), node (function test), shellcheck.

**Spec:** `docs/superpowers/specs/2026-11-23-custom-domain-design.md` — sections 2–7 for auth-gate; auth-app is plan B.

## Global Constraints

- Hostnames: AuthGate `api.<domain>`, Console `admin.<domain>`, Portal `portal.<domain>`; auth-app (plan B) the apex. rpIds equal the hostnames.
- SSM (String): `/credo/auth-gate/<stage>/domain` (e.g. `authgatehq.com`) and `/credo/auth-gate/<stage>/certificate-arn`. Absent `domain` → deploy exactly as today on the CloudFront address. `domain` present but `certificate-arn` absent, or the certificate not `ISSUED` → the deploy dies before `sam deploy` with a message naming `00-cert.sh`.
- Certificate: ACM in `us-east-1`, domain `<domain>`, SAN `*.<domain>`, DNS validation, tag `app=auth-gate`.
- SES: domain identity `<domain>` in `ap-southeast-1`, Easy DKIM `RSA_2048_BIT`. Sender `AuthGate <no-reply@<domain>>`; the display name is added in code; `EMAIL_FROM` and the IAM `ses:FromAddress` condition stay the bare address.
- DNS (printed, never written by code): validation CNAME; CNAME `api`, `admin`, `portal`, `@`; three `<token>._domainkey` CNAMEs to `<token>.dkim.amazonses.com`; `_dmarc` TXT `v=DMARC1; p=none`. Every record DNS only. No Cloudflare API token anywhere.
- Redirect: on the Console's and Portal's distributions, a request whose `Host` is not the app hostname gets `301` to `https://<hostname><uri>[?<query>]`; AuthGate's distribution does not redirect.
- Route 53 leaves the AuthGate template and `infra/deployer-policy-edge.json`; ACM rights stay.
- `register-client.sh stg` never overwrites a stored key without `--replace` and a typed confirmation.
- No step calls AWS. Never touch `~/.aws`. Secrets never on argv or printed. English; Conventional Commits ending `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `pnpm run check` passes at the end of every task.

## Review Focus

- `domain` set but `certificate-arn` missing, or the certificate still `PENDING_VALIDATION` → every deploy stops with a clear message instead of silently deploying on the old address. Test in Task 1.
- `00-cert.sh` rerun: a `PENDING_VALIDATION` or `ISSUED` certificate for the domain is reused (same validation record printed); a `FAILED`, `EXPIRED` or `REVOKED` one is ignored and a new one requested. Test in Task 1.
- Redirect keeps the path and every query value (repeated keys, encoded characters); a request already on the hostname still gets the SPA rewrite. Test in Task 3.
- `register-client.sh stg --replace` run while the stack's `BaseUrl` is still the CloudFront address (domain configured but not yet deployed) refuses: the new rpId would be wrong. Test in Task 4.
- `dns-records.sh` with a stack not deployed or SES DKIM not started prints that record as `pending`, never an empty or guessed value. Test in Task 5.

---

### Task 1: Certificate, domain configuration and the shared reader

**Files:**
- Modify: `scripts/setup-and-deploy/00-cert.sh` (rewritten)
- Create: `scripts/domain/lib.sh`, `scripts/domain/test/00-cert.sh`, `scripts/domain/test/lib.sh`
- Modify: `package.json` (`lint` adds `scripts/domain/*.sh scripts/domain/test/*.sh`; `test` adds both new tests)

**Interfaces:**
- `00-cert.sh <domain> [--wait]`, admin profile (`ADMIN_PROFILE`, default `default`): finds a certificate in `us-east-1` whose `DomainName` is `<domain>` and status is `ISSUED` or `PENDING_VALIDATION` (via `acm list-certificates --certificate-statuses`), else requests one (`--domain-name <domain> --subject-alternative-names '*.<domain>' --validation-method DNS --tags Key=app,Value=auth-gate`); prints the validation CNAME (name, value) and the ARN; writes the two SSM parameters (String, `--overwrite`) in `ap-southeast-1` under `/credo/auth-gate/$STAGE/`; with `--wait` polls `describe-certificate` every 15 s up to 30 minutes until `ISSUED`, dies on `FAILED`.
- `scripts/domain/lib.sh` (sourced): `domain_config` sets `DOMAIN` and `CERT_ARN` from SSM (`ParameterNotFound` → both empty; any other error dies); when `DOMAIN` is set it dies unless `CERT_ARN` is set and `acm describe-certificate --region us-east-1` reports `ISSUED`. `app_host <prefix>` echoes `<prefix>.<DOMAIN>` (or `<DOMAIN>` for an empty prefix).

- [ ] **Step 1: Write the failing tests** (stub `aws` like `scripts/setup-and-deploy/test/99-purge.sh`): `00-cert.sh` — no certificate → one `request-certificate` with the SAN and tag, both `put-parameter` calls; an `ISSUED` one listed → no request, same ARN written; a `FAILED` one only → a request; `--wait` with statuses pending, pending, issued → exits 0 after 3 polls; `--wait` with `FAILED` → non-zero. `lib.sh` — `domain` absent → `DOMAIN` and `CERT_ARN` empty, exit 0; `domain` present, ARN absent → dies naming `00-cert.sh`; certificate `PENDING_VALIDATION` → dies; `ISSUED` → both set; an SSM `AccessDenied` → dies; `app_host api` → `api.authgatehq.com`.
- [ ] **Step 2:** Run them, see them fail; implement; run again: all ok; `pnpm exec shellcheck -x scripts/domain/*.sh scripts/domain/test/*.sh scripts/setup-and-deploy/00-cert.sh`.
- [ ] **Step 3: Gate and commit** — `pnpm run check`; `git commit -m "feat(domain): one certificate for the apex and the wildcard, and its configuration in ssm"`.

### Task 2: AuthGate on api.<domain>; Route 53 leaves

**Files:**
- Modify: `infra/template.yaml` (remove `HostedZoneId`, its condition and the `AWS::Route53::RecordSet`), `infra/deployer-policy-edge.json` (remove the four `route53:*` actions), `scripts/setup-and-deploy/03-deploy.sh` (source `scripts/domain/lib.sh`, call `domain_config`; with `DOMAIN` set pass `DomainName=$(app_host api)` and `CertificateArn=$CERT_ARN` and skip the two-pass `DistributionBaseUrl` logic; drop `HOSTED_ZONE_ID` and the old `DOMAIN_NAME`/`CERTIFICATE_ARN` env inputs), README lines that mention them.

- [ ] **Step 1:** Edit; `sam validate --lint -t infra/template.yaml`; the policy file stays valid JSON under 6144 non-whitespace characters; shellcheck.
- [ ] **Step 2:** A stub-`aws` + stub-`sam` test `scripts/domain/test/03-deploy.sh`: with no `domain` the `sam deploy` overrides have no `DomainName`; with `domain` and an issued certificate they contain `DomainName=api.authgatehq.com` and the ARN, and only one `sam deploy` runs.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(domain): authgate deploys on api.<domain>; route 53 is gone"`.

### Task 3: Console and Portal on their hostnames, with the redirect

**Files:**
- Modify: `infra/console.yaml`, `infra/portal.yaml` (parameters `DomainName` and `CertificateArn` default `""`, condition `HasDomain`; distribution `Aliases` and `ViewerCertificate` (`SslSupportMethod: sni-only`, `MinimumProtocolVersion: TLSv1.2_2021`) under `HasDomain`; `BaseUrl` output `https://${DomainName}` under `HasDomain`; a `DistributionDomain` output (`!GetAtt Distribution.DomainName`) that `dns-records.sh` reads; `SpaRewrite` `FunctionCode` via `!Sub` with the hostname so it redirects as in Global Constraints before the existing rewrite — with no domain the substituted hostname is empty and the check is skipped)
- Modify: `scripts/console/deploy.sh`, `scripts/portal/deploy.sh` (call `domain_config`; with `DOMAIN` pass `DomainName=$(app_host admin|portal)` and `CertificateArn`; the Portal also passes `PublicBaseUrl=https://$(app_host portal)` and skips reading its own output for it)
- Create: `infra/functions/test/spa-rewrite.test.mjs` (node:test) and a tiny extractor that reads each template's `FunctionCode`, substitutes the hostname, and evaluates `handler`; wire it into `package.json` `test` (`node --test infra/functions/test`)

- [ ] **Step 1: Write the failing test** for both templates: host `dxxx.cloudfront.net`, uri `/clients/x`, querystring `{a:{value:'1',multiValue:[{value:'1'},{value:'2'}]},q:{value:'a b'}}` → status 301 and `location` `https://admin.authgatehq.com/clients/x?a=1&a=2&q=a%20b`; host `admin.authgatehq.com`, uri `/clients/x` → request with uri `/index.html`; host `admin.authgatehq.com`, uri `/assets/a.js` → unchanged; empty substituted hostname → no redirect for any host.
- [ ] **Step 2:** Implement; `node --test infra/functions/test`; `sam validate --lint` on both templates; the Portal CSP pin test still passes.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(domain): the console and the portal on admin. and portal.<domain>, old hosts redirected"`.

### Task 4: New clients for the new rpIds

**Files:**
- Modify: `scripts/console/register-client.sh`, `scripts/portal/register-client.sh` (`stg --replace`: when `domain_config` sets `DOMAIN`, require the stack's `BaseUrl` host to equal `app_host admin|portal`, else die "deploy with the domain first"; prompt `Type 'replace <stack>' to register a new client and overwrite the stored key:`; on the exact answer register, then write the key with `put-parameter --overwrite` through the existing 0600 file; print the new id and "disable the old client <old id> in the Console once the new one works"; any other answer changes nothing)
- Create: `scripts/domain/test/register-client.sh` (both scripts, stub `aws` and `curl`)

- [ ] **Step 1: Write the failing test:** no `--replace` with a stored key → refuses, no POST; `--replace` with the wrong answer → no POST, no `put-parameter`; `--replace` with the right answer → one POST whose body has `rpId` `admin.authgatehq.com` and origin `https://admin.authgatehq.com`, one `put-parameter --overwrite`; `--replace` while `BaseUrl` is still a cloudfront.net host → dies before any POST; the admin key and the new client key never appear in any argv.
- [ ] **Step 2:** Implement; tests pass; shellcheck.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(domain): register-client can replace the console's and the portal's client for a new rpid"`.

### Task 5: SES domain identity, the sender's name, and the DNS table

**Files:**
- Create: `scripts/domain/ses-identity.sh` (admin profile; `sesv2 create-email-identity --email-identity <domain> --dkim-signing-attributes NextSigningKeyLength=RSA_2048_BIT`; `AlreadyExistsException` → continue; prints the identity's verification and DKIM status)
- Create: `scripts/domain/dns-records.sh` (read only, admin profile; prints a table `name  type  content  note` for: the certificate's validation CNAME (from `describe-certificate`), `api`/`admin`/`portal` CNAMEs to the `DistributionDomain` (or the host of the distribution) output of `auth-gate-<stage>`, `auth-gate-console-<stage>`, `auth-gate-portal-<stage>`, `@` to `auth-app-<stage>`'s `DistributionDomain` output, the three DKIM CNAMEs from `get-email-identity` `DkimAttributes.Tokens`, `_dmarc` TXT `v=DMARC1; p=none`; every line notes `DNS only`; a missing source prints `pending (<why>)`)
- Modify: `internal/portal/mail/mail.go` (`NewSES(api, from string)` stores the bare address; `Send` sets `FromEmailAddress` to `AuthGate <` + from + `>`), its test
- Modify: `package.json` lint/test entries
- Create: `scripts/domain/test/dns-records.sh`

- [ ] **Step 1: Write the failing tests:** Go — `Send` uses `AuthGate <no-reply@example.com>` for `from` `no-reply@example.com`. Shell — all sources present → 9 lines whose names are the labels Cloudflare's form takes, relative to the zone (`_<validation>`, `api`, `admin`, `portal`, `@`, `<token>._domainkey` ×3, `_dmarc`; a trailing `.<domain>.` is stripped from ACM's record name), with types and contents; console stack absent → its line `pending (stack auth-gate-console-stg not deployed)`; no DKIM tokens → three lines collapse into one `pending (run ses-identity.sh)`.
- [ ] **Step 2:** Implement; tests pass; shellcheck; `make check`.
- [ ] **Step 3: Gate and commit** — `git commit -m "feat(domain): ses identity with dkim, the sender's name, and the cloudflare record table"`.

### Task 6: Docs and rollout

**Files:** `docs/memo/custom-domain.md` (decision record: one certificate; DNS only and why; one rpId per app; new clients instead of an rpId change; DKIM and DMARC; the old-host redirect); `README.md` ("Custom domain": what each app's name is, the scripts, the rollout of spec §7 as paste-ready blocks using `read` prompts and subshells, the SES production-access text of spec §5, the clean-up list, and that factors under old clients must be enrolled again); remove remaining README mentions of Route 53 / `HOSTED_ZONE_ID`.

- [ ] **Step 1:** Write them against the code of Tasks 1–5 (read it; check every command's flags, e.g. `sesv2 ... --email-identity`).
- [ ] **Step 2: Gate and commit** — `pnpm run check`; `git commit -m "docs: the custom domain, its records, and its rollout"`.
