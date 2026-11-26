// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { ReasonDialog } from '../src/screens/ReasonDialog';
import { TenantDetail } from '../src/screens/TenantDetail';
import { SessionProvider } from '../src/session';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });
const me = { username: 'ann', role: 'master', stage: 'full', mustChangePassword: false, factors: { pin: true, webauthn: [] } };
const TID = '01JD0000000000000000000T01';

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

async function mount(ui: React.ReactNode) {
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    // env="test" renders Modal content in place and without transitions.
    root!.render(
      <MantineProvider env="test" withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter initialEntries={[`/tenants/${TID}`]}><ToastProvider><SessionProvider>{ui}</SessionProvider></ToastProvider></MemoryRouter>
      </MantineProvider>,
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 50)); });
}

function tenantApi(over: { owner?: Record<string, unknown>; tenant?: Record<string, unknown> }) {
  vi.stubGlobal('fetch', async (url: string) => {
    if (url === '/api/me') return json(me);
    if (url.startsWith(`/api/tenants/${TID}/audit`)) return json({ entries: [] });
    if (url === `/api/tenants/${TID}`) {
      return json({
        tenant: { tenantId: TID, name: 'Acme', kind: 'org', createdAt: '2026-11-01T10:00:00Z', memberCount: 1, creatorEmail: 'o@acme.test', ...over.tenant },
        members: [], pendingInvites: 0,
        owner: { tenantId: TID, clientCount: 1, clientQuota: 5, ...over.owner },
        clients: { clients: [] },
      });
    }
    return json({ error: 'internal' }, 500);
  });
}

const page = () => (
  <Routes><Route path="/tenants/:id" element={<TenantDetail />} /></Routes>
);

// Why: a suspend that stopped half way (AuthGate done, Portal not) must be visible and repeatable, or a tenant stays blocked in one system only.
test('the tenant page warns Out of sync when AuthGate and the Portal disagree', async () => {
  tenantApi({ owner: { suspendedAt: '2026-11-20T10:00:00Z' } });
  await mount(page());
  expect(host!.textContent).toContain('Out of sync');
  expect([...host!.querySelectorAll('button')].some((b) => b.textContent === 'Suspend to finish')).toBe(true);
  await act(async () => root!.unmount());
  host!.remove();

  tenantApi({});
  await mount(page());
  expect(host!.textContent).not.toContain('Out of sync');
});

// Why: the reason is the only record of why a tenant or user was cut off; an empty one must never reach the backend.
test('the suspend dialog cannot be sent without a reason', async () => {
  const onConfirm = vi.fn();
  await mount(<ReasonDialog title="Suspend Acme" label="Reason" confirmLabel="Suspend" onCancel={() => {}} onConfirm={onConfirm} />);
  const area = document.querySelector('textarea')!;
  const send = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Suspend')!;
  expect(send.disabled).toBe(true);

  const type = async (v: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(area, v);
      area.dispatchEvent(new Event('input', { bubbles: true }));
    });
  };
  await type('   ');
  expect(send.disabled).toBe(true);
  await type('  abuse report 4411  ');
  expect(send.disabled).toBe(false);
  await act(async () => send.click());
  expect(onConfirm).toHaveBeenCalledWith('abuse report 4411');
});
