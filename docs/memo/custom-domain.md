# The custom domain: one certificate, DNS only, one rpId per app, new clients

## Decision

Every app of auth-gate has a name on `authgatehq.com`: AuthGate `api.`, the Console `admin.`, the Portal `portal.`, and auth-app on the apex. Five rules shape the move:

- One ACM certificate in `us-east-1` for the domain and `*.<domain>` serves every distribution.
- Every DNS record is DNS only (grey cloud at Cloudflare). Nothing proxies the traffic.
- Each app keeps its own rpId, equal to its hostname.
- AuthGate refuses an rpId change, so each app registers a new client instead.
- Email is signed by the domain: Easy DKIM, and a DMARC record at `p=none`.

The deploy scripts read the domain and the certificate ARN from SSM (`/credo/auth-gate/<stage>/domain` and `/certificate-arn`). A stack that was never deployed with a domain is unchanged and stays on its CloudFront address. The move is forward-only per stack: once a stack has been deployed with `DomainName` and `CertificateArn`, deleting the SSM parameter does not move it back, because `sam deploy` reuses the previous values. The way back is an admin `update-stack` that empties the two parameters (README, "If a step fails"), and for the Console and the Portal it ends at `--replace`, since their new clients carry the new rpId.

## Context

The apps ran on `*.cloudfront.net` and the Portal mailed from a personal address inside the SES sandbox, so strangers could not receive its emails. The domain is bought at Namecheap, and its nameservers are at Cloudflare (Free plan). The product owner fixed the names for the only stage there is, stg. If a production stage ever exists it takes these names and stg moves to `*.stg.authgatehq.com`.

## One certificate

CloudFront takes a certificate only from `us-east-1`. A SAN of `*.<domain>` makes one certificate cover `api`, `admin`, `portal` and any later name, and the apex and the wildcard share a single validation CNAME. One certificate means one record to add by hand and one ARN to keep. The validation CNAME stays at Cloudflare forever: ACM renews the certificate through it, and without it all three apps lose HTTPS about 13 months after issuance. auth-app reads the same ARN from its own parameter, so its deployer needs no right on auth-gate's parameters.

`00-cert.sh` lowercases its domain argument and reuses an `ISSUED` or `PENDING_VALIDATION` certificate for the domain whose SANs include `*.<domain>` and ignores a `FAILED`, `EXPIRED` or `REVOKED` one. Every deploy stops before `sam deploy` if the domain is set and the certificate is missing or not `ISSUED`, rather than deploying quietly on the old address.

## DNS only, and why

The scripts never write DNS and no Cloudflare API token exists anywhere: the records are few, set once, and printed by `scripts/domain/dns-records.sh`. Each one is DNS only because the apps rate-limit per viewer IP and write the IP to the audit log, both from the `CloudFront-Viewer-Address` header. A Cloudflare proxy in front would make every viewer look like a Cloudflare address, so the per-IP limits would collapse into one shared bucket and the audit log would lose the person. The apex is a CNAME: Cloudflare flattens it, DNS only included. The costs of staying grey are no Cloudflare WAF and no Cloudflare cache; CloudFront already is the door.

## One rpId per app

A passkey is bound to its rpId. If the Console, the Portal and the demo shared `authgatehq.com`, a passkey made for one could be offered to the others, and a break in one app's origin policy would reach all of them. With one rpId per hostname, the operators' passkeys, the customers' and the demo's never mix.

## New clients instead of an rpId change

AuthGate refuses to change a client's rpId, because the passkeys already enrolled are bound to it. Changing it for operators only would need its own design, and it is a non-goal. So `register-client.sh stg --replace` registers a new client with the new rpId and origin, and overwrites the stored client key only after a typed confirmation. It refuses while the stack's `BaseUrl` is still the CloudFront address, because the new rpId would be wrong.

Consequences, accepted:

- Every PIN and passkey under the old client is gone. Console admins, Portal users and the auth-app demo users enrol again. Until they do, whoever holds an account's password can enrol its first factor, so on stg the operator signs in right after each move.
- Tokens carry the new client id as audience. Tokens issued before live at most 15 minutes.
- The old clients are disabled at once and deleted about a week later. They are no longer the Console's own client, so the Console can delete them.
- Tenant clients are unaffected.

## The issuer moves

With a domain, AuthGate's `BASE_URL`, and so the JWT issuer, becomes `https://api.<domain>`. Apps still on the old issuer refuse the new tokens and the other way round, so the Console, the Portal and the auth-app demo (with only the new base URL, same client) are redeployed straight after AuthGate. On stg a short sign-in outage is accepted.

## DKIM and DMARC

The SES identity is the domain, with Easy DKIM (RSA 2048). Three `<token>._domainkey` CNAMEs to `<token>.dkim.amazonses.com` verify it, and then any `@authgatehq.com` sender works; the Portal sends as `AuthGate <no-reply@authgatehq.com>`. The display name is added in code, while `EMAIL_FROM` and the IAM `ses:FromAddress` condition stay the bare address.

There is no custom MAIL FROM: DKIM alignment on the domain is enough for DMARC to pass. DMARC starts as `v=DMARC1; p=none`, with no `rua`, because no mailbox exists to receive reports, and moves to `p=quarantine` after a few weeks of clean sending. There is no inbox: mail goes out from a no-reply address and no email asks for a reply.

## The old-host redirect

A passkey made on the new origin fails on the old one, so a stray visit to the CloudFront address must not stay there. The Console's and the Portal's existing `SpaRewrite` CloudFront Function answers `301` to `https://<hostname><path>[?<query>]` when the `Host` header is not the app's hostname, keeping the path and every query value, and a request already on the hostname still gets the SPA rewrite. AuthGate's distribution does not redirect: integrators call it backend to backend and keep working on both addresses during the move.

## Alternatives rejected

- Route 53 for the zone: a fixed monthly cost, and the zone already lives at Cloudflare.
- Cloudflare proxy (orange cloud): hides the viewer IP, see above.
- A Cloudflare API token so the scripts write records: a long-lived secret for a handful of records set once.
- One rpId for all apps: see above.
- A per-app certificate: three validations and three ARNs for no gain.
