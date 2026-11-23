# Portal email: a branded HTML layout, and mail sent to the domain

Date: 2026-11-23. Status: approved by the product owner in chat the same day. He reviewed a mockup of a link email and a notice email.

## 1. Purpose

The Portal sends 13 kinds of email (verify, reset, notices…) in English and Vietnamese, as plain text only. They work: SPF, DKIM and DMARC pass for `authgatehq.com`. But they look bare next to the Portal, and they have two faults:

- Every kind ends with "do not open the link above", including notices that have no link.
- No address at `authgatehq.com` receives mail. "Contact the operator" leads nowhere, and DMARC reports have nowhere to go.

This change gives every email a layout that matches the Portal, adds a plain-text part beside it, and makes four addresses at the domain deliver to the owner.

Decided by the product owner (not re-askable):

- The mockup's look: the Canopy palette, a white card on a pale green page, logo and wordmark above, a green primary button.
- A notice gets a secondary "Sign in" button.
- Email Routing ships in the same change, with `Reply-To: support@<domain>`.

**Done when:**
- every Portal email arrives with the layout below and a plain-text part, in both languages;
- Gmail's "Show original" still shows SPF, DKIM and DMARC as PASS;
- a reply to a Portal email reaches the owner's inbox through `support@`;
- `pnpm run check` passes and CI deploys green.

## 2. The layout

- **Structure.** One HTML layout for every kind, built with Go's `html/template`: tables, inline styles, a 560 px card. These are the forms Gmail, Outlook and Apple Mail render alike. From top to bottom:
  - a hidden preview line (the body's first sentence);
  - the logo and "AuthGate Portal";
  - the card: subject as the heading, the body, the `until` line when the kind has one (`account-locked`, `factor-reset-started`), the button, then for link kinds the fallback link;
  - a rule and the footer line (below);
  - under the card: the support line, the site, and why the mail was sent.
- **Colours** are the Canopy tokens of `packages/web-kit/src/theme.ts`: page `#eef4f0`, ink `#12201a`, muted `#4b5d54`, rule `#cfdcd5`, accent `#0b7041` with white text (about 6.2:1). There are no web fonts; the stack starts with `system-ui`.
- **Buttons:**
  - The four link kinds get a filled primary button that opens `link`:

    | Kind | English | Vietnamese |
    |---|---|---|
    | verify | Confirm email address | Xác nhận email |
    | reset | Choose a new password | Chọn mật khẩu mới |
    | factor-reset-confirm | Confirm the reset | Xác nhận đặt lại |
    | email-change-confirm | Confirm new address | Xác nhận địa chỉ mới |

  - Every notice except `account-deleted` gets an outlined "Sign in" / "Đăng nhập" button to `<site>/sign-in`.
- **Fallback link.** Link kinds keep the raw link under the button, in monospace, with "Button not working? Paste this link into your browser:" (VI: "Nút không bấm được? Dán liên kết này vào trình duyệt:").
- **Footer line**, which replaces `notYou`:
  - Link kinds: "If you did not ask for this, ignore this email; nothing changes until the link is opened." (VI: "Nếu bạn không yêu cầu, hãy bỏ qua thư này; không có gì thay đổi cho tới khi liên kết được mở.")
  - Notices: "If this was not you, change your password now. If you cannot sign in, write to `<support>`." (VI: "Nếu không phải bạn, hãy đổi mật khẩu ngay. Nếu không đăng nhập được, hãy viết cho `<support>`.")
  - Without a support address configured, the notice falls back to "contact the operator who runs this service".
- **Logo.** A 64×64 PNG made once from `portal-web/public/favicon.svg`, committed as `portal-web/public/email-logo.png`, served by the Portal at `<site>/email-logo.png`, and shown at 32×32 with alt text "AuthGate". Gmail does not render SVG in email.
- **Plain-text part.** It carries the same content in the same order: body, `until`, button label with the link, the footer line, the support line. Subjects are unchanged.

## 3. Sending

- SES `SendEmail` gets `Body.Html` and `Body.Text`.
- `ReplyToAddresses` is `[support]` when a support address is configured.
- The From stays `AuthGate <no-reply@…>`. The IAM condition `ses:FromAddress` is unchanged.
- New Portal configuration `SUPPORT_EMAIL`, template parameter `SupportEmail`, optional:
  - `scripts/portal/deploy.sh` sets it to `support@<domain>` when a domain is configured, and leaves it empty otherwise.
  - CI needs no new variable, because the domain already comes from SSM.
- The sender needs the site origin for the "Sign in" button and the logo. It takes `PUBLIC_BASE_URL`, which the Portal already has.
- `Vars` still hold only `link` and `until`, built by the server. Nothing a user typed reaches an email. `html/template` escapes them anyway, and `link` must start with the site origin or rendering fails.
- For local runs, the `Dir` sender writes `<name>.html` and `<name>.txt` beside its JSON file, so every kind can be opened in a browser.

## 4. Mail to the domain (Cloudflare Email Routing)

Free, and set up by hand once in the Cloudflare dashboard:

1. Enable Email Routing for `authgatehq.com`. Cloudflare adds its MX records, an SPF TXT at the apex and its own DKIM TXT.
2. Verify the owner's inbox as the destination.
3. Add routes for `support@`, `dmarc@`, `postmaster@` and `abuse@`. Leave the catch-all off.

Effects on the existing records:

- SES mail is unaffected. Its envelope sender is `amazonses.com`, so the apex SPF does not apply to it, and DMARC passes on DKIM as before.
- `_dmarc` becomes `v=DMARC1; p=none; rua=mailto:dmarc@authgatehq.com`. `dns-records.sh` prints that value.
- The MX, SPF and DKIM records Cloudflare adds are not printed, because Cloudflare manages them.

This overrides two non-goals of the custom-domain spec: inbound email and DMARC reports. That spec gains a pointer here.

## 5. Tests

- Every kind × language renders an HTML part and a text part.
- Only link kinds have the primary button and the fallback link.
- Only notices other than `account-deleted` have "Sign in".
- The footer line about the link appears only in link kinds.
- The support line and Reply-To appear only with a support address.
- A link holding `"<>&` is escaped in HTML.
- A link outside the site origin makes rendering fail.
- `Send` passes both parts and the Reply-To to SES.
- `dns-records.sh` prints the new `_dmarc`.
- The Portal's CSP pin test still passes. The logo is a static file and needs no CSP change.

## 6. Rollout

1. Email Routing in Cloudflare (§4), then a test mail to `support@` reaches the owner.
2. Change `_dmarc` in Cloudflare.
3. Deploy the Portal from the branch.
4. Send each kind that can be triggered from the browser: sign-up, reset, a factor added. Check them in Gmail and in one other client, and check "Show original".
5. Merge; CI redeploys.

The README gives the clicks and blocks for these steps. If the SES production-access request (custom-domain rollout step 9) is still unsent, its text gains: "Replies go to support@authgatehq.com, which a person reads."

## 7. Non-goals

- Open or click tracking of any kind.
- Remote images other than the logo.
- A dark-mode variant tuned per client.
- Web fonts.
- Mail from the Console.
- A mailbox at the domain beyond forwarding.
- Changing subjects or the wording of bodies.
