// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { Admins } from '../src/screens/Admins';
import { Audit } from '../src/screens/Audit';
import { Clients } from '../src/screens/Clients';
import { TenantDetail } from '../src/screens/TenantDetail';
import { Tenants } from '../src/screens/Tenants';
import { SessionProvider } from '../src/session';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });
const me = { username: 'ann', role: 'master', stage: 'full', mustChangePassword: false, factors: { pin: true, webauthn: [] } };

let root: Root | undefined;
let host: HTMLElement | undefined;

beforeEach(() => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
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

const TID = '01JD0000000000000000000T01';

async function mount(ui: React.ReactNode, data: Record<string, unknown>, path = '/') {
  vi.stubGlobal('fetch', async (url: string) => {
    if (url === '/api/me') return json(me);
    const key = Object.keys(data).find((k) => url.startsWith(k));
    return key ? json(data[key]) : json({ error: 'internal' }, 500);
  });
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter initialEntries={[path]}><ToastProvider><SessionProvider>{ui}</SessionProvider></ToastProvider></MemoryRouter>
      </MantineProvider>,
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
}

function expectCellLabels(table: HTMLTableElement) {
  const heads = [...table.querySelectorAll('thead th')];
  const rows = [...table.querySelectorAll<HTMLTableRowElement>('tbody tr')].filter((r) => !r.classList.contains('row-detail'));
  expect(rows.length).toBeGreaterThan(0);
  for (const row of rows) {
    const cells = [...row.cells];
    expect(cells.length).toBe(heads.length);
    cells.forEach((td, i) => {
      const th = heads[i]!;
      const text = th.textContent!.trim();
      if (text !== '' && !th.querySelector('.sr-only')) expect(td.dataset.label).toBe(text);
      else expect(td.dataset.label).toBeUndefined();
    });
    expect(row.querySelectorAll('.cell-title').length).toBe(1);
  }
}

// Why: on a phone the card shows data-label instead of the header; a renamed column must not leave a stale or missing label.
test('every table cell carries the label of its column, and each row has one title cell', async () => {
  await mount(<Clients />, { '/api/clients': { clients: [
    { clientId: 'c1', name: 'Billing app', rpId: 'example.com', createdAt: '2026-11-01T10:00:00Z', owner: '01JD0000000000000000000O01' },
    { clientId: 'c2', name: 'Other', rpId: 'other.com', createdAt: '2026-11-02T10:00:00Z', disabledAt: '2026-11-03T10:00:00Z' },
  ] } });
  expectCellLabels(host!.querySelector('table')!);
  await act(async () => root!.unmount());
  host!.remove();

  await mount(<Admins />, { '/api/admins': { admins: [
    { username: 'ann', role: 'master', mustChangePassword: false, lockedUntil: null, disabledAt: null, createdAt: '2026-11-01T10:00:00Z' },
    { username: 'bob', role: 'admin', mustChangePassword: true, lockedUntil: null, disabledAt: null, createdAt: '2026-11-02T10:00:00Z' },
  ] } });
  expectCellLabels(host!.querySelector('table')!);
  await act(async () => root!.unmount());
  host!.remove();

  await mount(<Audit />, { '/api/audit': { entries: [
    { adminId: 'a1', username: 'ann', action: 'login', target: '', result: 'success', ip: '10.0.0.1', source: 'web', at: '2026-11-01T10:00:00Z' },
    { adminId: '', username: '', action: 'login', target: '', result: 'failed:http_502', ip: '', source: '', at: '2026-11-02T10:00:00Z' },
  ] } });
  expectCellLabels(host!.querySelector('table')!);
  await act(async () => root!.unmount());
  host!.remove();

  await mount(<Tenants />, { '/api/tenants': { tenants: [
    { tenantId: TID, name: 'Acme', kind: 'org', createdAt: '2026-11-01T10:00:00Z', memberCount: 3, creatorEmail: 'o@acme.test', clientCount: 1, clientQuota: 5 },
    { tenantId: '01JD0000000000000000000T02', name: '', kind: 'personal', createdAt: '2026-11-02T10:00:00Z', memberCount: 1, creatorEmail: 'p@x.test', suspendedAt: '2026-11-03T10:00:00Z' },
  ] } });
  expectCellLabels(host!.querySelector('table')!);
  await act(async () => root!.unmount());
  host!.remove();

  // The tenant page has three tables: members, clients and its history.
  await mount(
    <Routes><Route path="/tenants/:id" element={<TenantDetail />} /></Routes>,
    {
      [`/api/tenants/${TID}/audit`]: { entries: [
        { actorId: 'operator', actorName: 'AuthGate administrator', action: 'tenant-quota', target: TID, result: 'success', ip: '', detail: { from: '3', to: '5' }, at: '2026-11-04T10:00:00Z' },
      ] },
      [`/api/tenants/${TID}`]: {
        tenant: { tenantId: TID, name: 'Acme', kind: 'org', createdAt: '2026-11-01T10:00:00Z', memberCount: 1, creatorEmail: 'o@acme.test' },
        members: [{ userId: 'u1', displayName: 'Olga', email: 'o@acme.test', role: 'owner', joinedAt: '2026-11-01T10:00:00Z' }],
        pendingInvites: 0, owner: { tenantId: TID, clientCount: 1, clientQuota: 5 },
        clients: { clients: [{ clientId: 'c1', name: 'Billing app', rpId: 'example.com', createdAt: '2026-11-01T10:00:00Z', owner: TID }] },
      },
    },
    `/tenants/${TID}`,
  );
  const tables = [...host!.querySelectorAll('table')];
  expect(tables.length).toBe(3);
  tables.forEach(expectCellLabels);
});
