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

1. With the WAF off, there is no WAF stack, no web ACL and no cost. Every CI deploy passes the
   sentinel `WebAclArn=none` to all three stacks.
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
- **Off is the sentinel `none`, never an empty value.** `sam` rejects an empty parameter override,
  and a parameter left out of an update keeps its previous value, so the scripts always pass
  `WebAclArn=none` or `WebAclArn=<arn>`.
- **The app stacks:** `infra/template.yaml`, `infra/portal.yaml` and `infra/console.yaml` each gain
  an optional parameter `WebAclArn`, defaulting to `none`, with an `AllowedPattern` that accepts
  `none` or a us-east-1 web ACL ARN. The condition `HasWaf` is `WebAclArn != none`. Each distribution sets
  `WebACLId: !If [HasWaf, !Ref WebAclArn, !Ref AWS::NoValue]`.
- **The deploy scripts:** `03-deploy.sh`, `scripts/portal/deploy.sh` and
  `scripts/console/deploy.sh` read the parameter on every deploy, including CI's, and pass
  `WebAclArn=<arn>` or `WebAclArn=none`.
  - Only `ParameterNotFound` means off. Any other error stops the deploy. This way, a read
    failure never quietly detaches a WAF that is on.

### `scripts/waf/enable.sh`

Run under the admin profile.

1. Print what it does and the cost from §2. Ask the owner to type `enable <stage>`. Anything else
   exits with no change.
2. Deploy `auth-gate-waf-<stage>` in `us-east-1`, then read its `WebAclArn` output.
3. Write the SSM parameter.
4. Print the next step: run the `ci` workflow by hand from the Actions tab, on `main`.

`enable.sh` does not deploy the apps itself. The owner's admin profile would otherwise change the
distributions out of band, and CI would be the second deployer. The `deploy` job runs on `workflow_dispatch`
on `main` only, reads the switch, and attaches the web ACL. It is safe to run `enable.sh` again,
because every step is idempotent.

### `scripts/waf/disable.sh`

Run under the admin profile, twice. AWS refuses to delete a web ACL that is still attached, and CI
is the only deployer of the distributions, so the script has two phases and picks one from the
state it finds:

1. **The switch is present.** Ask the owner to type `disable <stage>`, delete the SSM parameter,
   and print the next step. The owner then runs the `ci` workflow by hand on `main`; that deploy
   passes `WebAclArn=none` and detaches the web ACL.
2. **The switch is absent and the stack exists.** Ask for `disable <stage>` again. Refuse while
   any distribution still uses the web ACL (it lists them and tells the owner to run the
   workflow). Otherwise delete the stack and wait for the deletion to finish.

It refuses a broken stack (one that exists but has no `WebAclArn` output) and a failed switch
read, and never treats either as off.

### Shared rules for both scripts

- **No local app deploys.** Neither script builds or deploys the apps. The Actions run does.
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
    read access to that web ACL. AWS documents `UpdateDistribution` with the ARN in the
    distribution config as the path, and does not state further checks, so
    `infra/deployer-policy-edge.json` gains, defensively, the statements `ReadTheWebAcl`
    (`wafv2:GetWebACL` on `arn:aws:wafv2:us-east-1:<account>:global/webacl/auth-gate-*/*`) and
    `AttachTheWebAcl` (`cloudfront:AssociateDistributionWebACL` and
    `cloudfront:DisassociateDistributionWebACL` on the account's distributions, with the same
    `aws:ResourceTag/app` condition as the rest of the file).
  - `infra/role-boundary.json` and `infra/deployer-policy.json` need nothing: the admin creates the
    WAF stack, and the deployer makes no role for it.
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
  - `enable.sh` does nothing without the typed confirmation. With it, it deploys the stack, then
    writes the SSM switch.
  - `disable.sh` phase 1 deletes the switch and nothing else. Phase 2 deletes the stack only when no
    distribution uses the web ACL.
  - The deploy scripts pass `WebAclArn=none` when the parameter is absent and the ARN when it is
    present, and stop on any other SSM error.
  - `budget.sh` never prints the email address, and prints `exists` on a second run.
- **Template checks:** `sam validate --lint` (or the repository's template check) on
  `infra/waf.yaml` and on the three changed app templates.
- **The gate:** `pnpm run check`.
- **The owner's optional round on stg:** `enable.sh`, the Actions run, a `curl` to see the API still
  answer, then `disable.sh`, the Actions run, and `disable.sh` again. It costs a few cents. The
  branch can merge without it, because it ships off.

## 8. Docs

- `docs/reference/deploy.md`: a section "WAF (off by default)" with the commands, what they
  cost, and the order of `disable.sh` and the Actions run.
- `docs/memo/waf-off-by-default.md`: a decision record covering:
  - why it ships off;
  - why there is one web ACL;
  - why the per-IP limit for the API differs from the parent spec;
  - why the switch is a manual script and not a CI flag.
- `README.md`: one line pointing to the deploy section.
- The parent spec §9 gains a line saying this document refines it.
