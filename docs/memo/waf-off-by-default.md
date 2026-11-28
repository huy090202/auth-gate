# The WAF is built, but off

## Decision

A web ACL for all three distributions is built (`infra/waf.yaml`, stack `auth-gate-waf-<stage>` in `us-east-1`), and it ships off. The owner switches it on and off with `scripts/waf/enable.sh` and `scripts/waf/disable.sh`, under the admin profile. The switch is the SSM parameter `/credo/auth-gate/<stage>/waf-acl-arn`. Every CI deploy reads it and passes `WebAclArn=<arn>` or the sentinel `none` to the three stacks. How to run it is in [Deploy and CI](../reference/deploy.md#waf-off-by-default).

## Why it ships off

auth-gate has no users yet, and a web ACL costs money whether or not traffic arrives: 5 USD for the ACL, 1 USD per rule, 0.60 USD per million requests, about 9-10 USD a month. The owner will not pay a fixed cost for nobody. So off means no stack, no ACL and no cost, and turning it on takes minutes when abuse or real traffic arrives. A budget alert (`scripts/cost/budget.sh`, 10 USD a month) warns if spend still surprises.

## Why one web ACL

CloudFront web ACLs live in `us-east-1`, and one ACL can protect any number of distributions. One ACL for the API, the Portal and the Console costs 5 USD once, not three times. Rules that differ by host are one rule with a host condition, not one ACL each.

## Why the API's limit differs from the parent spec

The parent spec has one rule: 300 requests per 5 minutes per IP. The API is called by customers' backends from a few IP addresses, so one request a second would block a normal customer. The 300 limit applies to every host except `api.`, because the Portal and Console are also reachable through their CloudFront default hostnames, where the host redirect does not run. The API gets a flood guard of 60000 per 5 minutes per IP (about 200 requests a second). It is high because auth-gate's own Lambda-to-AuthGate calls and a customer's backend share egress IPs. Per-client limits live in AuthGate's token bucket ([rate limit](rate-limit-bucket.md)). IPv6 is aggregated per /128, a weak flood guard on the API distribution, which keeps IPv6. Aggregating by the client-key header was rejected, because a flood could vary the header to bypass the limit. The common rule set's body size check only counts, because a WebAuthn attestation can exceed 8 KB.

## Why the switch is a manual script and not a CI flag

- Creating a web ACL and a stack in `us-east-1` is not something CI may do. The CI deployer gains no right to create a web ACL, only to attach one (`AttachTheWebAcl`, `ReadTheWebAcl` in `infra/deployer-policy-edge.json`). These are read-only on the ACL and narrow on the distributions: AWS documents `UpdateDistribution` as the path and states no further checks, so the extra rights are defensive.
- CI is the only deployer of the distributions, so the owner's scripts do not deploy the apps. `enable.sh` writes the switch, and the owner runs `ci` by hand from the Actions tab on `main`. A repository variable would be a second switch that can disagree with the first.
- Off is the sentinel `none`, never an empty value. `sam` rejects an empty parameter override, and a parameter left out of an update keeps its previous value, so the scripts always pass one of the two.
- Disable is two phases, because AWS refuses to delete an attached web ACL: delete the switch, run `ci` to detach, then run `disable.sh` again to delete the stack. It refuses while a distribution still uses the ACL, and refuses a broken stack or a failed read instead of treating it as off.
- Only `ParameterNotFound` means off. Any other read error stops the deploy, so a flaky read never quietly detaches a WAF that is on.

## Cost

Nothing while off. About 9-10 USD a month while on. WAF logging, Bot Control, CAPTCHA and geographic rules are not built; each costs more.
