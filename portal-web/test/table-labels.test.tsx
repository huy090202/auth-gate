// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { WorkspacesContext, type Tenant } from '../src/lib/tenants';
import { Audit } from '../src/screens/Audit';
import { ClientList } from '../src/screens/Clients';
import { Members } from '../src/screens/Members';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

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
  await act(async () => { await new Promise((r) => setTimeout(r, 30)); });
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
  const rows = [{ clientId: 'c1', name: 'Billing app', rpId: 'example.com', createdAt: '2026-11-01T10:00:00Z' }];
  await mount(<ClientList rows={rows} capped={false} tenantId={ORG} />);
  expectCellLabels(host!.querySelector('table')!);
  await act(async () => root!.unmount());
  host!.remove();

  const tenants: Tenant[] = [{ tenantId: ORG, kind: 'org', name: 'Acme', role: 'owner' }];
  const invite = { inviteId: 'i1', email: 'new@x.com', role: 'member', invitedBy: { displayName: 'Olga' }, expiresAt: '2026-12-01T10:00:00Z' };
  vi.stubGlobal('fetch', async (url: string) => {
    if (url === `/api/t/${ORG}/members`) {
      return json({ members: [
        { userId: 'u-owner', displayName: 'Olga', email: 'olga@x.com', role: 'owner', joinedAt: '2026-11-01T10:00:00Z' },
        { userId: 'u-mem', displayName: 'Mark', email: 'mark@x.com', role: 'member', joinedAt: '2026-11-03T10:00:00Z' },
      ] });
    }
    if (url === `/api/t/${ORG}/invites`) return json({ invites: [invite] });
    return json({ error: 'internal' }, 500);
  });
  await mount(
    <WorkspacesContext.Provider value={{ tenants, reload: async () => {}, userId: 'u-owner', loading: false }}>
      <Routes><Route path="/t/:tenantId/members" element={<Members />} /></Routes>
    </WorkspacesContext.Provider>,
    `/t/${ORG}/members`,
  );
  const tables = [...host!.querySelectorAll('table')];
  expect(tables.length).toBe(2);
  tables.forEach(expectCellLabels);
  await act(async () => root!.unmount());
  host!.remove();

  vi.stubGlobal('fetch', async () => json({ entries: [
    { actorId: 'a1', actorName: 'Olga', action: 'client-create', target: 'c1', result: 'success', ip: '10.0.0.1', at: '2026-11-01T10:00:00Z' },
    { actorId: 'a2', action: 'client-update', target: '', result: 'failed:http_502', ip: '', at: '2026-11-02T10:00:00Z' },
  ] }));
  await mount(<Routes><Route path="/t/:tenantId/audit" element={<Audit />} /></Routes>, `/t/${ORG}/audit`);
  expectCellLabels(host!.querySelector('table')!);
});
