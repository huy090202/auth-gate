# Self-service P4b: the WAF, built but off — design

Date: 2026-11-28. Status: draft for the owner's review. Branch `feat/waf`.

This is the last piece of P4 in `2026-11-22-self-service-design.md`. It refines the parent
spec's §9 "WAF, built but off".

## 1. Purpose

The parent spec plans an AWS WAF in front of the service, built in advance but switched off.
auth-gate has no users yet, and a web ACL has a fixed monthly cost, so the WAF must cost nothing
until the owner switches it on. When abuse or real traffic arrives, the owner can switch it on
in minutes, and switch it off again.

Decided by the owner on 2026-11-27 and 2026-11-28:

- It is off by default, and off means no cost. In the owner's words: "mặc định là tắt cái waf
  nha, tại chưa có người dùng, tốn tiền quá thì tui hông chịu nỗi".
- When on, one web ACL protects all three distributions: AuthGate's API, the Portal and the
  Console.
- Only the owner turns it on or off, with scripts run under the admin profile. CI never creates
  the WAF.
- A cost alert emails the owner when the account's monthly spend is forecast to pass, or does
  pass, 10 USD.

Done means four things, all verified without switching the WAF on:

1. With the WAF off, there is no WAF stack, no web ACL and no cost. Every CI deploy passes an
   empty web ACL to all three stacks.
2. `scripts/waf/enable.sh` turns it on and `scripts/waf/disable.sh` turns it off. They run in the
   order §3 gives. Their tests use a stubbed `aws`.
3. A CI deploy while the WAF is on keeps the web ACL attached.
4. `scripts/cost/budget.sh` creates the 10 USD budget, once.

## 2. The web ACL

The web ACL lives in its own stack, `auth-gate-waf-<stage>`, in `us-east-1`. CloudFront requires
web ACLs there. The template is `infra/waf.yaml`, with `Scope: CLOUDFRONT` and name
`auth-gate-<stage>`. The default action is Allow. It has four rules:

| Priority | Rule | Scope | Action |
|---|---|---|---|
| 0 | `AmazonIpReputationList` (managed) | all | block |
| 1 | `AWSManagedRulesCommonRuleSet` (managed) | all | block, but `SizeRestrictions_BODY` is overridden to count |
| 2 | rate, 300 requests per 5 minutes per IP | Host starts with `portal.` or `admin.` | block |
| 3 | rate, 6000 requests per 5 minutes per IP | all | block |

- **Rule 1's override:** WebAuthn attestation bodies can exceed the 8 KB size check, so that check
  only counts. It does not block a passkey enrollment.
- **Rules 2 and 3 differ from the parent spec.** The parent spec has one 300-per-5-minutes rule.
  AuthGate's API is called by customers' backends from a few IP addresses, so 300 per 5 minutes
  (one request a second) would block a normal customer. The strict limit therefore applies only
  to the browser-facing hosts. The API gets a flood guard, and each client already has its own
  token bucket inside AuthGate.
- **Monitoring:** CloudWatch metrics and sampled requests are on, at no charge. Full WAF logging
  is off because it costs money.
- **Cost when on:** 5 USD for the web ACL, plus 1 USD per rule (4 USD), plus 0.60 USD per million
  requests. That is about 9 to 10 USD a month at this traffic. `enable.sh` prints this before it
  asks for confirmation.

## 3. The switch

- **The switch itself:** the SSM String parameter `/credo/auth-gate/<stage>/waf-acl-arn`, in the
  app region. When it is present, the WAF is on, and its value is the web ACL's ARN.
- **The app stacks:** `infra/template.yaml`, `infra/portal.yaml` and `infra/console.yaml` each gain
  an optional parameter `WebAclArn`, defaulting to `""`. Each distribution sets
  `WebACLId: !If [HasWaf, !Ref WebAclArn, !Ref AWS::NoValue]`.
- **The deploy scripts:** `03-deploy.sh`, `scripts/portal/deploy.sh` and
  `scripts/console/deploy.sh` read the parameter on every deploy, including CI's, and pass
  `WebAclArn` with the value or empty.
  - Only `ParameterNotFound` means off. Any other error stops the deploy. This way, a read
    failure never quietly detaches a WAF that is on.

### `scripts/waf/enable.sh`

Run under the admin profile.

1. Print what it does and the cost from §2. Ask the owner to type `enable <stage>`. Anything else
   exits with no change.
2. Deploy `auth-gate-waf-<stage>` in `us-east-1`, then read its `WebAclArn` output.
3. Write the SSM parameter.
4. Run the three app deploy scripts, so each distribution attaches the web ACL.
5. Print the state of each distribution's web ACL.

It is safe to run again, because every step is idempotent.

### `scripts/waf/disable.sh`

Run under the admin profile. AWS refuses to delete a web ACL that is still attached, so the
steps run in this order:

1. Ask the owner to type `disable <stage>`.
2. Delete the SSM parameter.
3. Run the three app deploy scripts, so each distribution detaches the web ACL.
4. Delete the stack and wait for the deletion to finish.
5. Print that the WAF is off and its cost has stopped.

### Shared rules for both scripts

- **The app deploys need a build:** the scripts run `02-build.sh` first, the same build CI runs.
- **A failure partway** stops the script with a message that names the next manual step. Running
  the script again finishes the job.

## 4. Cost alert

- **What `scripts/cost/budget.sh` creates:** a monthly COST budget named `auth-gate-monthly`, with
  a 10 USD limit, for the whole account.
- **Notifications:** one email when forecasted spend passes 80%, and one when actual spend
  passes 100%.
- **Email address:** it comes from `BUDGET_EMAIL`, an environment variable read only at run time.
  The address is never written to the repo or printed back.
- **Idempotent:** it prints `exists` when the budget is already there, and `created` otherwise.
- **Profile:** it runs under the admin profile.
- **Price:** the account's first two budgets cost nothing.

## 5. IAM

- **The CI deployer:**
  - Reads the new SSM parameter, which it already can: `/credo/auth-gate/*`.
  - Passes the ARN to CloudFront. Attaching a web ACL through `UpdateDistribution` also needs
    read access to that web ACL. `infra/deployer-policy-edge.json` gains the WAF read actions
    AWS requires for this, scoped to
    `arn:aws:wafv2:us-east-1:<account>:global/webacl/auth-gate-*/*`. The plan confirms the exact
    actions against AWS's documentation.
  - When that policy changes, the owner reruns `00-deployer.sh` before merge, as before.
- **The admin profile:** creates and deletes the WAF stack, the SSM switch and the budget. The CI
  deployer gains no right to create a web ACL, to create a stack in `us-east-1`, or to touch
  budgets.

## 6. Not in scope

- Turning the WAF on as part of this branch. It ships off.
- WAF logging, Bot Control, CAPTCHA, IP allow lists, and geographic rules.
- The auth-app's own distribution, which lives in a separate repository.
- A budget that stops spending. AWS Budgets only alerts.

## 7. Tests and checks

- **Shell tests with a stubbed `aws` and stubbed deploy scripts:**
  - `enable.sh` does nothing without the typed confirmation. With it, it runs stack, then SSM
    put, then the three deploys, in that order.
  - `disable.sh` runs SSM delete, then the three deploys, then stack delete, in that order.
  - The deploy scripts pass `WebAclArn=` when the parameter is absent and the ARN when it is
    present, and stop on any other SSM error.
  - `budget.sh` never prints the email address, and prints `exists` on a second run.
- **Template checks:** `sam validate --lint` (or the repository's template check) on
  `infra/waf.yaml` and on the three changed app templates.
- **The gate:** `pnpm run check`.
- **The owner's optional round on stg:** `enable.sh`, a `curl` to see the API still answer, then
  `disable.sh`. It costs a few cents. The branch can merge without it, because it ships off.

## 8. Docs

- `docs/reference/deploy.md`: a section "WAF (off by default)" with the two commands, what they
  cost, and the order of `disable.sh`.
- `docs/memo/waf-off-by-default.md`: a decision record covering:
  - why it ships off;
  - why there is one web ACL;
  - why the per-IP limit for the API differs from the parent spec;
  - why the switch is a manual script and not a CI flag.
- `README.md`: one line pointing to the deploy section.
- The parent spec §9 gains a line saying this document refines it.
