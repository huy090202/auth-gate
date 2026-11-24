// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { can, roleChoices } from '../src/lib/orgs';
import { WorkspacesContext, type Role, type Tenant } from '../src/lib/tenants';
import { Members } from '../src/screens/Members';
import { OrgSettings } from '../src/screens/OrgSettings';
import { SessionProvider } from '../src/session';
import { Shell } from '../src/shell';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const PERSONAL = '01JD0000000000000000000P01';
const ORG = '01JD0000000000000000000O01';
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });

let root: Root | undefined;
let host: HTMLElement | undefined;

beforeEach(() => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  // jsdom has no layout; Mantine's dropdown scrolls the active option into view.
  Element.prototype.scrollIntoView = () => {};
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
});

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = undefined;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 30)); });

async function mount(ui: React.ReactNode, at: string) {
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter initialEntries={[at]}>
          <ToastProvider>{ui}</ToastProvider>
        </MemoryRouter>
      </MantineProvider>,
    );
  });
  await settle();
}

const tenants = (role: Role): Tenant[] => [
  { tenantId: PERSONAL, kind: 'personal', name: '', role: 'owner' },
  { tenantId: ORG, kind: 'org', name: 'Acme', role },
];
const withWorkspaces = (role: Role, ui: React.ReactNode) => (
  <WorkspacesContext.Provider value={{ tenants: tenants(role), reload: async () => {}, userId: 'u-me', loading: false }}>{ui}</WorkspacesContext.Provider>
);

const members = [
  { userId: 'u-owner', displayName: 'Olga', email: 'olga@x.com', role: 'owner', joinedAt: '2026-11-01T10:00:00Z' },
  { userId: 'u-me', displayName: 'Me', email: 'me@x.com', role: 'admin', joinedAt: '2026-11-02T10:00:00Z' },
  { userId: 'u-mem', displayName: 'Mark', email: 'mark@x.com', role: 'member', joinedAt: '2026-11-03T10:00:00Z' },
];

function stubOrg(list = members) {
  vi.stubGlobal('fetch', async (url: string) => {
    if (url === `/api/t/${ORG}/members`) return json({ members: list });
    if (url === `/api/t/${ORG}/invites`) return json({ invites: [] });
    return json({ error: 'internal' }, 500);
  });
}
async function mountMembers(role: Role) {
  stubOrg();
  await mount(
    withWorkspaces(role, <Routes><Route path="/t/:tenantId/members" element={<Members />} /></Routes>),
    `/t/${ORG}/members`,
  );
}

const roleSelects = () => [...host!.querySelectorAll('input[aria-label^="Role of"]')] as HTMLInputElement[];
const rowOf = (name: string) => [...host!.querySelectorAll('tbody tr')].find((r) => r.textContent!.includes(name))!;
const button = (scope: ParentNode, label: string) => [...scope.querySelectorAll('button')].find((b) => b.textContent === label);

// Why: the role table decides which controls a screen shows; a wrong row would offer an admin a
// way to demote an owner, or hide a control the backend would allow.
test('can and roleChoices follow the role table', () => {
  for (const a of ['invite', 'change-role', 'remove', 'rename', 'delete', 'manage-client'] as const) expect(can('member', a, 'member')).toBe(false);
  expect(roleChoices('member', 'member')).toEqual([]);
  expect(roleChoices('admin', 'owner')).toEqual([]);
  expect(roleChoices('admin', 'member')).toEqual(['admin', 'member']);
  expect(roleChoices('owner', 'member')).toEqual(['owner', 'admin', 'member']);
  expect(can('admin', 'remove', 'owner')).toBe(false);
  expect(can('admin', 'remove', 'member')).toBe(true);
  expect(can('admin', 'invite')).toBe(true);
  expect(can('admin', 'invite', 'owner')).toBe(false);
  expect(can('owner', 'invite', 'owner')).toBe(true);
  expect(can('admin', 'manage-client')).toBe(true);
  expect(can('admin', 'rename')).toBe(false);
  expect(can('admin', 'delete')).toBe(false);
  expect(can('owner', 'delete')).toBe(true);
  expect(can('owner', 'rename')).toBe(true);
});

// Why: a plain member must see the roster and nothing that would only end in a 403.
test('a member sees the members but no Invite, role Select or Remove', async () => {
  await mountMembers('member');
  expect(host!.textContent).toContain('Mark');
  expect(button(host!, 'Invite')).toBeUndefined();
  expect(roleSelects()).toEqual([]);
  expect(button(host!, 'Remove')).toBeUndefined();
});

// Why: an admin cannot touch an owner; and the invite dialog must not offer the owner role the
// backend refuses an admin (403 forbidden).
test('an admin gets controls on a member but none on the owner, and no owner role to invite', async () => {
  await mountMembers('admin');
  expect(rowOf('Olga').querySelector('input[aria-label^="Role of"]')).toBeNull();
  expect(button(rowOf('Olga'), 'Remove')).toBeUndefined();
  expect(rowOf('Mark').querySelector('input[aria-label^="Role of"]')).not.toBeNull();
  expect(button(rowOf('Mark'), 'Remove')).toBeDefined();

  await act(async () => button(host!, 'Invite')!.click());
  await settle();
  const dialog = document.querySelector('.mantine-Modal-body')!;
  const roleInput = dialog.querySelector('input[aria-label="Role"]') as HTMLInputElement;
  await act(async () => roleInput.click());
  const list = document.getElementById(roleInput.getAttribute('aria-controls')!)!;
  const options = [...list.querySelectorAll('[role="option"]')].map((o) => o.textContent);
  expect(options).toEqual(['Admin', 'Member']);
});

// Why: a personal workspace has no members or settings, so the links must not lead to a 400.
test('the shell shows Members and Settings for an organisation only, and Audit to owners and admins', async () => {
  const me = {
    userId: 'u-me', email: 'a@x.com', displayName: 'Ann', lang: 'en' as const, stage: 'full' as const, personalTenantId: PERSONAL,
    factors: { pin: true, webauthn: [] }, recoveryCodesLeft: 10,
  };
  const shellAt = async (role: Role, id: string) => {
    vi.stubGlobal('fetch', async (url: string) => (url === '/api/tenants' ? json({ tenants: tenants(role) }) : json({}, 404)));
    await mount(
      <SessionProvider>
        <Routes><Route path="/t/:tenantId/clients" element={<Shell me={me}><p>hi</p></Shell>} /></Routes>
      </SessionProvider>,
      `/t/${id}/clients`,
    );
    const has = (section: string) => host!.querySelector(`a[href="/t/${id}/${section}"]`) !== null;
    const r = { members: has('members'), settings: has('settings'), audit: has('audit') };
    await act(async () => root!.unmount());
    host!.remove();
    return r;
  };
  expect(await shellAt('owner', PERSONAL)).toEqual({ members: false, settings: false, audit: true });
  expect(await shellAt('member', ORG)).toEqual({ members: true, settings: true, audit: false });
});

// Why: deleting an organisation destroys its clients; the exact name is the only brake before the step-up.
test('Delete organisation stays disabled until the exact name is typed', async () => {
  vi.stubGlobal('fetch', async () => json({}, 404));
  await mount(withWorkspaces('owner', <Routes><Route path="/t/:tenantId/settings" element={<OrgSettings />} /></Routes>), `/t/${ORG}/settings`);
  await act(async () => button(host!, 'Delete organisation')!.click());
  await settle();
  const body = document.querySelector('.mantine-Modal-body')!;
  const confirm = () => body.querySelectorAll('button')[1] as HTMLButtonElement;
  const type = async (v: string) => {
    const input = body.querySelector('input') as HTMLInputElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, v);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
  };
  expect(confirm().disabled).toBe(true);
  await type('acme');
  expect(confirm().disabled).toBe(true);
  await type('Acme');
  expect(confirm().disabled).toBe(false);
});

// Why: a hard reload of an organisation page runs before the workspace list arrives; it must wait,
// not say "not found" about an organisation the user belongs to.
test('Settings waits for the workspace list instead of reporting not found', async () => {
  let release!: () => void;
  const held = new Promise<void>((r) => { release = r; });
  vi.stubGlobal('fetch', async (url: string) => {
    if (url === '/api/tenants') {
      await held;
      return json({ tenants: tenants('owner') });
    }
    return json({}, 404);
  });
  const me = {
    userId: 'u-me', email: 'a@x.com', displayName: 'Ann', lang: 'en' as const, stage: 'full' as const, personalTenantId: PERSONAL,
    factors: { pin: true, webauthn: [] }, recoveryCodesLeft: 10,
  };
  await mount(
    <SessionProvider>
      <Routes><Route path="/t/:tenantId/settings" element={<Shell me={me}><OrgSettings /></Shell>} /></Routes>
    </SessionProvider>,
    `/t/${ORG}/settings`,
  );
  expect(host!.querySelector('[role="alert"]')).toBeNull();
  expect(button(host!, 'Delete organisation')).toBeUndefined();
  release();
  await settle();
  expect(button(host!, 'Delete organisation')).toBeDefined();
});
