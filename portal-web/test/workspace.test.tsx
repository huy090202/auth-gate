// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { MAX_PAGES, fetchAllClients, type ClientView } from '../src/lib/clients';
import { auditUrl } from '../src/lib/audit';
import { WorkspacesContext, type Role, type Tenant } from '../src/lib/tenants';
import { ClientDetail } from '../src/screens/ClientDetail';
import { ClientList } from '../src/screens/Clients';
import { SessionProvider } from '../src/session';
import { Shell } from '../src/shell';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const TENANT = '01JD0000000000000000000T01';
const CLIENT = '01JD0000000000000000000C01';
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });

let root: Root | undefined;
let host: HTMLElement | undefined;
const calls: { url: string; method: string }[] = [];

beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
});

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = undefined;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

async function mount(ui: React.ReactNode, at = '/') {
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
  await act(async () => {
    await new Promise((r) => setTimeout(r, 30));
  });
}

const buttons = () => [...document.querySelectorAll('button')];
const button = (label: string) => buttons().find((b) => b.textContent === label);

// Why: an empty workspace must say what to do next, and a list the walk gave up on must say so, or a
// user would read a partial list as the whole of their clients.
test('the client list shows the empty state, and a capped list says it may be incomplete', async () => {
  await mount(<ClientList rows={[]} capped={false} tenantId={TENANT} />);
  expect(host!.textContent).toContain('No clients yet.');
  expect(host!.querySelector('table')).toBeNull();
  await act(async () => root!.unmount());
  host!.remove();

  const rows = [{ clientId: CLIENT, name: 'Billing app', rpId: 'example.com', createdAt: '2026-11-01T10:00:00Z' }];
  await mount(<ClientList rows={rows} capped tenantId={TENANT} />);
  expect(host!.textContent).toContain('Billing app');
  expect(host!.textContent).toContain('Only the first 1 clients were loaded');
  expect(host!.querySelector(`a[href="/t/${TENANT}/clients/${CLIENT}"]`)).not.toBeNull();
});

test('fetchAllClients stops at MAX_PAGES and reports the list as capped; the cursor is encoded', async () => {
  const urls: string[] = [];
  vi.stubGlobal('fetch', async (url: string) => {
    urls.push(url);
    return json({ clients: [], nextCursor: 'a#b' });
  });
  const r = await fetchAllClients(TENANT);
  expect(r.capped).toBe(true);
  expect(urls.length).toBe(MAX_PAGES);
  expect(urls[1]).toBe(`/api/t/${TENANT}/clients?cursor=a%23b`);
});

test('the audit cursor contains # and is URL-encoded', () => {
  expect(auditUrl(TENANT, 'T#x#9')).toBe(`/api/t/${TENANT}/audit?cursor=T%23x%239`);
  expect(auditUrl(TENANT, '')).toBe(`/api/t/${TENANT}/audit`);
});

const view = (over: Partial<ClientView> = {}): ClientView => ({
  clientId: CLIENT, name: 'Billing app', rpId: 'example.com', origins: ['https://example.com'],
  pinPolicy: { length: 6, maxAttempts: 5, lockoutSec: 900 }, tokenTtlSec: 300, createdAt: '2026-11-01T10:00:00Z', ...over,
});

/** The viewer's role in TENANT, an organisation, comes from the shell's workspace list. */
async function mountDetail(c: ClientView, role: Role = 'owner') {
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    calls.push({ url, method: init?.method ?? 'GET' });
    if (url === `/api/t/${TENANT}/clients/${CLIENT}` && (init?.method ?? 'GET') === 'GET') return json(c);
    return json({ error: 'internal' }, 500);
  });
  const tenants: Tenant[] = [{ tenantId: TENANT, kind: 'org', name: 'Acme', role }];
  await mount(
    <WorkspacesContext.Provider value={{ tenants, reload: async () => {}, userId: 'u-me', loading: false }}>
      <Routes><Route path="/t/:tenantId/clients/:id" element={<ClientDetail />} /></Routes>
    </WorkspacesContext.Provider>,
    `/t/${TENANT}/clients/${CLIENT}`,
  );
}

async function type(value: string) {
  const input = document.querySelector('.mantine-Modal-body input') as HTMLInputElement;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    set.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

// Why: deleting a client stops its keys for good; the typed name is the only brake, and the step-up
// must not even open on a near miss.
test('deleting a client needs its exact name, then a step-up; nothing is sent before', async () => {
  await mountDetail(view());
  await act(async () => button('Delete client')!.click());
  await act(async () => {
    await new Promise((r) => setTimeout(r, 30));
  });
  const confirm = () => document.querySelector('.mantine-Modal-body')!.querySelectorAll('button')[1] as HTMLButtonElement;
  expect(confirm().disabled).toBe(true);
  for (const near of ['billing app', 'Billing app ', 'Billing']) {
    await type(near);
    expect(confirm().disabled, near).toBe(true);
  }
  await type('Billing app');
  expect(confirm().disabled).toBe(false);
  await act(async () => confirm().click());
  await act(async () => {
    await new Promise((r) => setTimeout(r, 30));
  });
  expect(document.body.textContent).toContain('Confirm it is you'); // the step-up dialog
  expect(calls.filter((c) => c.method === 'DELETE')).toEqual([]);
});

// Why: AuthGate answers 409 disabled_by_operator to an Enable the tenant has no right to; the
// screen must not offer it, and must say why.
test('a client an administrator disabled shows the lock and no Enable action', async () => {
  await mountDetail(view({ disabledAt: '2026-11-10T10:00:00Z', disabledBy: 'operator' }));
  expect(host!.textContent).toContain('Locked by an administrator');
  expect(button('Enable client')).toBeUndefined();
  expect(button('Disable client')).toBeUndefined();
  expect(document.querySelectorAll('style').length).toBe(0);
});

// Why: a member who is shown Disable, Purge or Delete goes through type-to-confirm and a step-up only
// to be refused with 403 (parent spec section 6); an admin must still have them.
test('a member sees no Disable, Purge or Delete; an admin does', async () => {
  const manage = ['Disable client', 'Clear factors', 'Delete client'];
  await mountDetail(view(), 'member');
  for (const label of manage) expect(button(label), label).toBeUndefined();
  expect(button('Rotate key')).toBeDefined();
  await act(async () => root!.unmount());
  host!.remove();

  await mountDetail(view(), 'admin');
  for (const label of manage) expect(button(label), label).toBeDefined();
});

test('a client the tenant disabled can be enabled', async () => {
  await mountDetail(view({ disabledAt: '2026-11-10T10:00:00Z', disabledBy: 'tenant' }));
  expect(button('Enable client')).toBeDefined();
  expect(host!.textContent).not.toContain('Locked by an administrator');
});

// Why: the production CSP has no inline styles; a Mantine part that injects a <style> is silent until
// the browser refuses it. The shell (Select, Menu) and the screens above must not add one.
test('the shell lists the user\'s workspaces in the switcher and injects no <style>', async () => {
  vi.stubGlobal('fetch', async (url: string) =>
    url === '/api/tenants' ? json({ tenants: [{ tenantId: TENANT, kind: 'personal', name: 'ignored', role: 'owner' }] }) : json({}, 404));
  const me = {
    userId: 'u1', email: 'a@x.com', displayName: 'Ann', lang: 'en' as const, stage: 'full' as const, personalTenantId: TENANT,
    factors: { pin: true, webauthn: [] }, recoveryCodesLeft: 10,
  };
  await mount(
    <SessionProvider>
      <Shell me={me}><p id="inner">hi</p></Shell>
    </SessionProvider>,
    `/t/${TENANT}/clients`,
  );
  const input = host!.querySelector('input[aria-label="Workspace"]') as HTMLInputElement;
  expect(input.value).toBe('Personal');
  expect(host!.querySelector(`a[href="/t/${TENANT}/audit"]`)).not.toBeNull();
  expect(host!.querySelector('#inner')).not.toBeNull();
  expect(document.querySelectorAll('style').length).toBe(0);
});
