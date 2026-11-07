// @vitest-environment jsdom
import { ActionIcon, Button, MantineProvider, Modal, SegmentedControl, Select } from '@mantine/core';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, expect, test, vi } from 'vitest';
import { SchemeToggle } from '../src/App';
import { mantineVarsCss } from '../src/lib/mantine-vars';
import { ShownOnce } from '../src/ShownOnce';
import { Admins } from '../src/screens/Admins';
import { Audit } from '../src/screens/Audit';
import { ClientDetail } from '../src/screens/ClientDetail';
import { Clients } from '../src/screens/Clients';
import { SessionProvider, type Me } from '../src/session';
import { Shell } from '../src/shell';
import { theme } from '../src/theme';

const varsFile = join(import.meta.dirname, '../src/mantine-vars.css');

test('mantine-vars.css matches mantineVarsCss()', () => {
  // `pnpm --filter console-web gen:vars` sets GEN_VARS and rewrites the file.
  if (process.env.GEN_VARS) writeFileSync(varsFile, mantineVarsCss());
  expect(readFileSync(varsFile, 'utf8')).toBe(mantineVarsCss());
});

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
// jsdom has no scrollIntoView; the open list of a Select scrolls its active option into view.
Element.prototype.scrollIntoView = () => {};

afterEach(() => {
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

test('no <style> element is rendered, including with a Modal open', async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="auto" theme={theme}>
        <Button>Open</Button>
        <ActionIcon aria-label="icon">x</ActionIcon>
        <SegmentedControl value="en" onChange={() => {}} data={['en', 'vi']} />
        <Modal opened onClose={() => {}} title="Dialog" transitionProps={{ duration: 0 }}>
          body
        </Modal>
      </MantineProvider>,
    );
  });
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});

test('a Select renders no <style> element with its dropdown open', async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <Select label="Role" value="admin" onChange={() => {}} data={[{ value: 'admin', label: 'Admin' }, { value: 'master', label: 'Master' }]}
          comboboxProps={{ transitionProps: { duration: 0 } }} defaultDropdownOpened />
        <Select label="Long" value="a" onChange={() => {}} searchable data={['a', 'b', 'c']} comboboxProps={{ transitionProps: { duration: 0 } }} defaultDropdownOpened />
      </MantineProvider>,
    );
  });
  expect(document.querySelectorAll('[role="option"]').length).toBeGreaterThanOrEqual(5);
  expect(document.querySelector('[role="option"][aria-selected="true"]')?.textContent).toContain('Admin');
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});

test('clicking the scheme toggle renders no <style> element', async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <SchemeToggle />
      </MantineProvider>,
    );
  });
  const button = host.querySelector('button')!;
  await act(async () => {
    button.click();
  });
  expect(document.documentElement.getAttribute('data-mantine-color-scheme')).toBe('dark');
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});

test('index.html has no inline script', () => {
  const html = readFileSync(join(import.meta.dirname, '../index.html'), 'utf8');
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)];
  expect(scripts.length).toBeGreaterThanOrEqual(2);
  for (const [, attrs, body] of scripts) {
    expect(attrs).toMatch(/\bsrc="/);
    expect(body!.trim()).toBe('');
  }
  expect(html).toMatch(/<script src="\/theme-init\.js"><\/script>\s*<script type="module"/);
  expect(html).not.toMatch(/\son[a-z]+=|style=/i);
});

for (const collapsed of [false, true]) test(`the signed-in Shell renders no <style> element (collapsed: ${collapsed})`, async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal('fetch', async () => new Response('{"error":"unauthorized"}', { status: 401 }));
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  const me: Me = { username: 'ann', role: 'master', stage: 'full', mustChangePassword: false, factors: { pin: true, webauthn: [] } };
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter>
          <SessionProvider>
            <Shell me={me} collapsed={collapsed}>content</Shell>
          </SessionProvider>
        </MemoryRouter>
      </MantineProvider>,
    );
  });
  expect(host.querySelector('nav a[aria-current="page"], nav a')).not.toBeNull();
  expect(host.querySelectorAll('nav a').length).toBe(4);
  for (const a of host.querySelectorAll('nav a')) expect(a.getAttribute('title')).toBeTruthy();
  expect(host.querySelector('.sidebar-collapse')?.getAttribute('aria-expanded')).toBe(String(!collapsed));
  // The avatar menu opens with My account and Sign out, and still renders no <style>.
  await act(async () => {
    host.querySelector<HTMLButtonElement>('.avatar-btn')!.click();
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 400)); // the menu's own transition
  });
  expect([...document.querySelectorAll('[role="menuitem"]')].map((e) => e.textContent)).toEqual(['My account', 'Sign out']);
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});

// Why these render the real screens: a Mantine component that injects a <style> (Table.ScrollContainer,
// a Select dropdown, responsive props) only fails in a browser under the production CSP, where it is
// silent in the page and loud only in the console. The last assertions are a smoke check that rows with
// an empty source, no detail or an unknown action and reason render at all, with the raw value as text.
const master: Me = { username: 'ann', role: 'master', stage: 'full', mustChangePassword: false, factors: { pin: true, webauthn: [] } };
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'content-type': 'application/json' } });
const ID = '01HZZZZZZZZZZZZZZZZZZZZZZZ';
const routes: Record<string, unknown> = {
  '/api/me': master,
  '/api/clients': { clients: [{ clientId: ID, name: 'Shop', rpId: 'shop.example.com', createdAt: '2026-11-01T10:00:00Z' }] },
  [`/api/clients/${ID}`]: {
    clientId: ID, name: 'Shop', rpId: 'shop.example.com', origins: ['https://shop.example.com'],
    pinPolicy: { length: 6, maxAttempts: 5, lockoutSec: 900 }, tokenTtlSec: 300, rateLimit: { burst: 100, perSecond: 50 },
    createdAt: '2026-11-01T10:00:00Z',
  },
  '/api/audit': {
    month: '2026-11',
    entries: [
      { adminId: 'a1', username: 'ann', action: 'update-client', target: ID, result: 'success', ip: '203.0.113.9', source: 'web', at: '2026-11-05T09:00:00.000000000Z',
        detail: { fields: 'name,pinPolicy', name: 'Shop', pinPolicy: 'length=6,maxAttempts=5,lockoutSec=900' } },
      { adminId: '', username: '', action: 'login', target: '', result: 'failed:invalid_credentials', ip: '', source: '', at: '2026-11-05T08:00:00.000000000Z',
        detail: { reason: 'wrong_password' } },
      { adminId: '', username: 'cli:root', action: 'brand-new-action', target: '', result: 'failed:brand_new_code', ip: '', source: 'cli', at: '2026-11-04T08:00:00.000000000Z' },
    ],
  },
  '/api/admins': { admins: [
    { username: 'ann', role: 'master', mustChangePassword: false, lockedUntil: null, disabledAt: null, createdAt: '2026-10-01T00:00:00Z' },
    { username: 'bob', role: 'admin', mustChangePassword: true, lockedUntil: null, disabledAt: null, createdAt: '2026-10-02T00:00:00Z' },
  ] },
};

async function renderAt(path: string) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal('fetch', async (url: string) => {
    const p = url.split('?')[0]!;
    return Object.hasOwn(routes, p) ? json(routes[p]) : new Response('{"error":"not_found"}', { status: 404 });
  });
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter initialEntries={[path]}>
          <SessionProvider>
            <Routes>
              <Route path="/clients" element={<Clients />} />
              <Route path="/clients/:id" element={<ClientDetail />} />
              <Route path="/audit" element={<Audit />} />
              <Route path="/admins" element={<Admins />} />
            </Routes>
          </SessionProvider>
        </MemoryRouter>
      </MantineProvider>,
    );
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 50));
  });
  return { host, root };
}

for (const path of ['/clients', `/clients/${ID}`, '/admins']) test(`the ${path.split('/')[1]} screen renders no <style> element`, async () => {
  const { host, root } = await renderAt(path);
  expect(host.querySelector('h1')).not.toBeNull();
  expect(host.querySelectorAll('button').length).toBeGreaterThan(1);
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});

test('the audit screen renders no <style> element, expanded rows included, and falls back to raw values', async () => {
  const { host, root } = await renderAt('/audit');
  expect(host.querySelectorAll('tbody tr:not(.row-detail)').length).toBe(3);
  for (const b of host.querySelectorAll<HTMLButtonElement>('.row-toggle')) {
    await act(async () => {
      b.click();
    });
  }
  const text = host.textContent ?? '';
  expect(text).toContain('Client updated'); // a known action: its label
  expect(text).toContain('Wrong password'); // detail.reason: its label
  expect(text).toContain('brand-new-action'); // an unknown action: the raw value
  expect(text).toContain('brand_new_code'); // an unknown failure: the raw value
  expect(host.querySelectorAll('.row-detail[data-open="true"]').length).toBe(3);
  expect(document.querySelectorAll('style').length).toBe(0);
  // Each of the four filters opens its list and picks an option, still with no <style>.
  const selects = [...host.querySelectorAll<HTMLInputElement>('input.mantine-Select-input')];
  expect(selects.length).toBe(4);
  for (const input of selects) {
    await act(async () => {
      input.click();
    });
    expect(document.querySelectorAll('[role="option"]').length).toBeGreaterThan(1);
    expect(document.querySelectorAll('style').length).toBe(0);
    await act(async () => {
      document.querySelectorAll<HTMLElement>('[role="option"]')[1]!.click();
    });
  }
  await act(async () => root.unmount());
});

test('ShownOnce renders no <style> element', async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <ShownOnce title="t" label="l" value="v" onClose={() => {}} />
      </MantineProvider>,
    );
  });
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});
