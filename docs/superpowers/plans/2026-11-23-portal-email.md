# Portal email Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every Portal email goes out as a branded HTML part plus a plain-text part. Replies reach the owner through `support@<domain>`.

**Architecture:**
- `internal/portal/mail` renders both parts from one embedded `html/template` layout and the existing per-kind strings. The senders carry a `Site` (base URL, support address), and the SES sender sets Reply-To.
- The Portal gets an optional `SUPPORT_EMAIL`, which the deploy script derives from the configured domain.
- DNS and docs gain Email Routing and the DMARC `rua`.

**Tech Stack:** Go (`html/template`, `embed`, aws-sdk-go-v2 sesv2), SAM, bash with stub-`aws` tests.

**Spec:** `docs/superpowers/specs/2026-11-23-portal-email-design.md`

## Global Constraints

- English in code, comments, docs and commits. One Conventional Commit per task, with the body ending in `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- No new Go module dependency in `go.mod`: only `html/template` and `embed` from the standard library.
- Subjects and the existing body sentences of all 13 kinds stay word for word (`templates.go`). The one exception: a body's trailing `:` becomes `.` because a button now follows it.
- Colours, exactly:
  - page `#eef4f0`
  - card `#ffffff` with a 1px `#cfdcd5` border and radius 10px
  - ink `#12201a`
  - muted `#4b5d54`
  - rule `#cfdcd5`
  - accent `#0b7041`, with white button text
  - font stack `system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif`, no web fonts
  - card width 560px
- No remote image except `<base>/email-logo.png`. No tracking of any kind.
- `Vars` stay limited to `link` and `until`. A `link` must start with `<base>/`; anything else is a render error.
- Secrets never on argv or in output. No real AWS calls by implementers.

## Review Focus

1. Images blocked (Outlook, some Gmail settings): the mail still reads. The logo has `alt="AuthGate"` and the wordmark is text. Pinned in Task 1.
2. A long token link: the fallback link wraps instead of widening the card (`word-break:break-all`). Pinned in Task 1.
3. Vietnamese diacritics: both SES parts carry `Charset: UTF-8`, and the HTML declares `<meta charset="utf-8">`. Pinned in Task 1.
4. No support address (local runs, a stage without a domain): no `mailto:`, no Reply-To, and the notice footer falls back to "contact the operator…". Pinned in Task 1.
5. An off-site or look-alike link (`https://portal.example.evil/x` for base `https://portal.example`) is refused. Pinned in Task 1.

---

### Task 1: Render both parts, and send them

**Files:**
- Create: `internal/portal/mail/layout.html` (embedded)
- Modify: `internal/portal/mail/templates.go` (new strings), `internal/portal/mail/mail.go` (Site, Rendered, Render, SES, Dir)
- Modify callers: `cmd/portal/main.go:62,79`, `internal/portal/signup_test.go:45` and every other `mail.NewDir`/`mail.NewSES`/`Render` caller (`grep -rn "mail.New\|Render(" --include=*.go`)
- Test: `internal/portal/mail/mail_test.go`

**Interfaces:**
- Produces:
  - `type Site struct{ BaseURL, Support string }`. `BaseURL` has no trailing slash. `Support` is a bare address or `""`.
  - `type Rendered struct{ Subject, Text, HTML string }`
  - `func Render(m Message, s Site) (Rendered, error)`
  - `func NewSES(api SESAPI, from string, s Site) *SES`
  - `func NewDir(dir string, s Site) (*Dir, error)`
- Task 2 wires `Site{BaseURL: cfg.PublicBaseURL, Support: cfg.SupportEmail}`. Until then `main.go` passes `Support: ""`.

**Strings.** Exact copy from the spec, added to `templates.go`:

- Primary buttons:

  | Kind | en | vi |
  |---|---|---|
  | verify | Confirm email address | Xác nhận email |
  | reset | Choose a new password | Chọn mật khẩu mới |
  | factor-reset-confirm | Confirm the reset | Xác nhận đặt lại |
  | email-change-confirm | Confirm new address | Xác nhận địa chỉ mới |

- Secondary button: "Sign in" / "Đăng nhập", to `<base>/sign-in`. It goes on every non-link kind except `account-deleted`.
- Fallback line: "Button not working? Paste this link into your browser:" / "Nút không bấm được? Dán liên kết này vào trình duyệt:"
- Link-kind footer: "If you did not ask for this, ignore this email; nothing changes until the link is opened." / "Nếu bạn không yêu cầu, hãy bỏ qua thư này; không có gì thay đổi cho tới khi liên kết được mở."
- Notice footer, with support: "If this was not you, change your password now. If you cannot sign in, write to %s." / "Nếu không phải bạn, hãy đổi mật khẩu ngay. Nếu không đăng nhập được, hãy viết cho %s."
- Notice footer, without support: "If this was not you, change your password now. If you cannot sign in, contact the operator who runs this service." / "Nếu không phải bạn, hãy đổi mật khẩu ngay. Nếu không đăng nhập được, hãy liên hệ người vận hành dịch vụ này."
- Support line, only with support: "Questions? Write to %s." / "Cần hỗ trợ? Viết cho %s."
- Why line, where `<host>` is BaseURL without its scheme: "AuthGate · <host> · Sent because this address was used on the AuthGate Portal." / "AuthGate · <host> · Thư này gửi đến bạn vì địa chỉ này được dùng trên AuthGate Portal."
- Remove `notYou`. Keep `untilLabel`.

**Layout.** Follow the approved mockup, `docs/superpowers/specs/2026-11-23-portal-email-mockup.html`. Its inline SVG logo becomes the PNG `<img>`, and its sample texts become the strings above. Structure: tables and inline styles only; `lang` set on `<html>`.
- the hidden preheader;
- logo `<img src="<base>/email-logo.png" width="32" height="32" alt="AuthGate">` beside the text "AuthGate Portal";
- the card, containing:
  - `<h1>` with the subject;
  - the body;
  - the until line, if any;
  - the button (primary filled, or secondary outlined: 1px `#0b7041` border, `#0b7041` text);
  - for link kinds, the fallback line and the link, in monospace with `word-break:break-all`;
  - an `<hr>` and the footer line;
- under the card: the support line (with a `mailto:` link) and the why line.

The preheader is the body up to and including its first `.` that is followed by a space or by the end. A trailing `:` counts as `.`.

**Text part order:**
1. the body, with a trailing `:` made `.`;
2. `untilLabel + until`, if present;
3. `<button label>: <link>` for link kinds, `<Sign in label>: <base>/sign-in` for notices that have the button;
4. the footer line;
5. the support line, if present.

Blocks are separated by one blank line.

**Senders:**
- `SES.Send` sets `Body.Text` and `Body.Html`, both with `Charset: "UTF-8"`. It sets `ReplyToAddresses: []string{s.Support}` only when `Support != ""`. `FromEmailAddress` stays `"AuthGate <" + from + ">"`.
- `Dir.Send` keeps the JSON file and its fields `to, lang, kind, subject, text`, and adds `html`. It writes `<same base name>.html` and `.txt` beside it, each with mode 0600 and `O_EXCL`.

- [ ] **Step 1: Write the failing tests** in `mail_test.go`. Use `site := Site{BaseURL: "https://portal.example", Support: "support@example.com"}` and `link := "https://portal.example/verify?t=abc"`.
  - `TestRenderEveryKindInBothLanguages`. For each kind × `en`/`vi`:
    - `Subject` is non-empty with no CR/LF.
    - `HTML` contains `<meta charset="utf-8">`, `alt="AuthGate"`, `AuthGate Portal` and the subject inside `<h1`.
    - Both parts contain `support@example.com`. Neither contains `link above` nor `liên kết ở trên`.
    - Link kinds: the HTML contains the kind's button label, `href="https://portal.example/verify?t=abc"` and `word-break:break-all`. The text contains `<label>: https://portal.example/verify?t=abc`. Both contain the link-kind footer.
    - Notices other than `account-deleted`: both parts contain `https://portal.example/sign-in` and the Sign-in label. Both contain the notice footer with `support@example.com`.
    - `account-deleted`: neither part contains `/sign-in`.
    - Keep the `len(Kinds) != 13` check.
  - `TestRenderWithoutSupport`: with `Site{BaseURL: "https://portal.example"}`, no part contains `mailto:` or `Questions?`, and the `account-locked` en text contains `contact the operator who runs this service`.
  - `TestRenderPreheader`: the verify/en HTML contains `Open this link within 24 hours to confirm your email address and finish creating your account.`
  - `TestRenderUntil`: `account-locked` en with `until: "2026-11-23 10:00 UTC"`. Both parts contain `Until: 2026-11-23 10:00 UTC`.
  - `TestRenderEscapesTheLink`: link `https://portal.example/x?a="<b>&c=1`. The HTML contains no `<b>` and no `"<b`. The render does not fail.
  - `TestRenderRefusesOffSiteLinks`: links `https://evil.example/x`, `https://portal.example.evil/x` and `http://portal.example/x` each make `Render` return an error.
  - Keep the existing cases (needs a link, unknown var, line break), moved to the new signature.
  - `TestSESSendsBothPartsAndReplyTo`: using the existing fake:
    - `Body.Text` and `Body.Html` are non-nil, and both charsets are `UTF-8`.
    - From is `AuthGate <no-reply@example.com>`.
    - `ReplyToAddresses` is `["support@example.com"]`, and empty when `Support` is `""`.
  - `TestDirWritesHTMLAndText`: the JSON has non-empty `html` and `text`. Files with the same base name and the extensions `.html` and `.txt` exist, with mode `0600`.
- [ ] **Step 2: Run** `go test ./internal/portal/mail/`. Expected: build failure (new signatures).
- [ ] **Step 3: Implement** in `mail.go`, `templates.go` and `layout.html`.
  - Parse the template once at package init with `template.Must`.
  - The origin check is `strings.HasPrefix(link, s.BaseURL+"/")` with `s.BaseURL != ""`.
  - The `/sign-in` URL is built the same way.
  - Update every caller. `signup_test.go` uses `mail.NewDir(dir, mail.Site{BaseURL: "https://portal.test"})`.
- [ ] **Step 4: Run** `go test ./internal/portal/... ./cmd/portal/...`. Expected: PASS, including the existing reset and signup tests, which read `.Text` from the mailbox.
- [ ] **Step 5: Look at it.** Render verify/en, signup-exists/vi and account-locked/en into a temp dir. Open them in a browser, or print the paths for the controller, and compare with the mockup's order. Remove the temp files.
- [ ] **Step 6: Gate and commit.** `pnpm run check`, then `git commit -m "feat(portal): branded html email with a plain-text part, reply-to support"`.

### Task 2: Support address, the logo, and the deploy

**Files:**
- Modify: `internal/portal/config.go`, its test (`internal/portal/app_test.go`, where config is tested), `cmd/portal/main.go`
- Modify: `infra/portal.yaml` (parameter, env), `scripts/portal/deploy.sh`
- Create: `portal-web/public/email-logo.png`
- Test: `scripts/domain/test/portal-deploy.sh`

**Interfaces:**
- Consumes: `mail.Site` (Task 1).
- Produces:
  - `Config.SupportEmail string`, from env `SUPPORT_EMAIL`;
  - template parameter `SupportEmail`;
  - deploy override `SupportEmail=support@<domain>`.

- [ ] **Step 1: Write the failing tests.**
  - Go:
    - With `SUPPORT_EMAIL=support@example.com`, the config holds it.
    - With `SUPPORT_EMAIL` unset, it is empty and the config is valid.
    - `SUPPORT_EMAIL` set to `Name <a@b.c>`, `a b@c.d` or `nodomain` is a config error.
  - Shell, in `portal-deploy.sh`, following `scripts/domain/test/03-deploy.sh`:
    - Stub `aws`: SSM returns domain `authgatehq.com` and a certificate ARN; ACM answers `ISSUED`; `describe-stacks` answers the outputs the script reads.
    - Stub `sam` records its argv and exits 1, so the script stops after it.
    - Assert that the recorded args contain `SupportEmail=support@authgatehq.com`.
    - Add a no-domain case (SSM `ParameterNotFound`): the args contain no `SupportEmail=`.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - `config.go`: an optional `SupportEmail`. When set, it must match `^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$`.
  - `main.go`: passes `mail.Site{BaseURL: cfg.PublicBaseURL, Support: cfg.SupportEmail}` to both senders.
  - `portal.yaml`: parameter `SupportEmail` (String, default `""`, AllowedPattern `^([^@\s<>]+@[^@\s<>]+\.[^@\s<>]+)?$`) and env `SUPPORT_EMAIL: !Ref SupportEmail`.
  - `deploy.sh`: with `$DOMAIN` set, `params+=("SupportEmail=support@$DOMAIN")`. sam rejects empty overrides, so with no domain nothing is passed and a previous value is kept. Update the header comment.
- [ ] **Step 4: The logo.** Make a 64×64 PNG from `portal-web/public/favicon.svg` with a throwaway Go program in a temp directory, using `github.com/srwiley/oksvg` and `github.com/srwiley/rasterx` in that directory's own module, not in this repo's `go.mod`. Commit only `portal-web/public/email-logo.png`. Check with `file portal-web/public/email-logo.png`, which should report `PNG image data, 64 x 64`, and view it to confirm it matches the favicon.
- [ ] **Step 5: Run.**
  - The tests: expected PASS.
  - `sam validate --lint -t infra/portal.yaml`: expected valid.
  - `pnpm -C portal-web build`: expected `dist/email-logo.png` present.
- [ ] **Step 6: Gate and commit.** `pnpm run check`, then `git commit -m "feat(portal): support address from the domain, and the email logo"`.

### Task 3: DMARC reports, Email Routing and the docs

**Files:**
- Modify: `scripts/domain/dns-records.sh:103`, `scripts/domain/test/dns-records.sh:105`
- Modify: `README.md` (the `## Custom domain` section and the Portal deploy notes), `docs/memo/custom-domain.md` (DKIM and DMARC paragraph), `docs/superpowers/specs/2026-11-23-custom-domain-design.md` §8 (a pointer: inbound mail and DMARC reports moved to the portal-email spec)

**Interfaces:** consumes nothing in code.

- [ ] **Step 1: Failing test.** In `dns-records.sh`'s test, `_dmarc` expects `_dmarc TXT v=DMARC1; p=none; rua=mailto:dmarc@example.com DNS only`, using the domain the test configures.
- [ ] **Step 2: Implement.** `dns-records.sh` prints `v=DMARC1; p=none; rua=mailto:dmarc@$DOMAIN`. Run the test: expected PASS.
- [ ] **Step 3: Docs.** Add a README subsection "Mail to the domain" inside `## Custom domain`:
  - The Cloudflare clicks of spec §4, as numbered steps:
    1. Email, Email Routing, Enable (accept the records it adds);
    2. Destination addresses, add the owner's inbox and open the verification mail;
    3. Routing rules, Custom address, one rule per `support`, `dmarc`, `postmaster` and `abuse`, action Send to an email, the verified destination;
    4. Catch-all stays disabled.
  - Why SES is unaffected.
  - The `_dmarc` edit, with `rua`.
  - The rollout of spec §6 as numbered steps, with paste-ready blocks for the Portal deploy (from the existing step 6 block, without the register lines) and the `dns-records.sh` check.
  - `SUPPORT_EMAIL` in the Portal configuration notes.
  - In the SES production-access text, the sentence "Replies go to support@authgatehq.com, which a person reads."
  - Update the memo and the custom-domain spec §8 the same way.
- [ ] **Step 4: Gate and commit.** `pnpm run check`, then `git commit -m "docs(email): mail to the domain, dmarc reports, and the email rollout"`.
