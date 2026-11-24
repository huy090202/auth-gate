# Portal and Console on a phone — design

Date: 2026-11-24. Status: draft, for the owner's review. Branch `feat/mobile`.

## 1. Purpose

A leaked client key should be revocable from a phone, without a laptop. Today both apps
work below 768 px, but only just. The sidebar turns into a top bar where the navigation
wraps over two lines, tables scroll sideways with no hint that they can, and an action row
squeezes its button beside a long paragraph. In the audit log, for example, the Action
column is cut off and nothing on the screen says the table scrolls.

Done means three things:

- The incident path works at 375 px in both apps, with no sideways scrolling and no zoom:
  sign in (password, then PIN or passkey), open a client, rotate its key or disable it,
  confirm with step-up, and copy the new key from the shown-once dialog.
- Every other signed-in screen reads at 375 px without sideways scrolling.
- Wide screens (769 px and up) look exactly as they do today.

## 2. Scope

Everything lives in `packages/web-kit`, so both apps get it. App code changes only to
pass labels into the kit. There is no backend, infra or cost change.

### 2.1 Shell, up to 768 px

- **Header row:** a menu button, the brand, and the avatar menu. The language switch and
  the theme toggle leave the header and move into the drawer.
- **Drawer:** the menu button opens a Mantine `Drawer` from the left. It holds:
  - the Portal's workspace switcher;
  - the navigation, with each link at least 44 px tall;
  - the language switch and the theme toggle.

  The drawer closes when a link is chosen, when the route changes, on Esc, and on a tap
  outside it. Focus moves into it while it is open and returns to the button when it closes.
- The sidebar's collapse button and the icon-only rail exist only on wide screens, as today.

### 2.2 Tables, up to 640 px

The six tables are the Portal's Clients, Members, pending invites and Audit, and the
Console's Clients, Admins and Audit. On a phone:

- Each row becomes a card. The first cell is the card's title.
- Each other cell is one line with a label and a value. The label comes from a `data-label`
  attribute that the app sets from the same string as the column header.
- An actions cell sits at the bottom of the card, and its buttons fill the width.
- Audit rows still expand to show their details.

This is done with CSS on the existing `.data-table` markup, so there is no second component.

### 2.3 Screens and dialogs, up to 640 px

- An `.action-row` (rotate, disable, purge, delete, leave the organisation, delete the
  account) wraps: the text comes first, then the button at full width.
- Form inputs use a 16 px font. Below that size, iOS Safari zooms the page on focus.
- Modals keep a 12 px margin from the screen edges. The shown-once dialog stacks its parts:
  - the key, wrapped onto as many lines as it needs;
  - the copy button, at full width;
  - the checkbox;
  - the Done button.
- `.toolbar` children take the full width: the search fields, the filters, and the
  New client and Invite buttons.

## 3. Not in scope

- Gestures, an installable app (PWA), or a bottom tab bar.
- A separate mobile layout for the sign-in pages. They are single cards and already shrink
  below 520 px. They get the 16 px inputs and are checked in the round in §4.
- Native apps.

## 4. Testing

- Unit tests (vitest) cover only behaviour that CSS cannot carry:
  - the drawer closes after a navigation, and opens again from the menu button;
  - each table cell's `data-label` matches its column header, so a renamed column cannot
    leave a stale label on phones.
- Screenshots: Claude renders both apps at 375 × 812 and at 1280 × 800 with Playwright,
  against the Vite build, with the API mocked in the script. These are kept in the
  scratchpad and are not added to the repository. The wide shots are compared with `main`
  to prove §1's third point.
- The owner's round on a real phone, in EN and VI, light and dark: the incident path in §1,
  in the Portal and in the Console.
- The gate: `pnpm run check`.
