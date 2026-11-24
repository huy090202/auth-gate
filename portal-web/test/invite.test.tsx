// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { pendingInvite } from '../src/lib/invite';
import { Invite } from '../src/screens/Invite';
import { SessionProvider } from '../src/session';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const ORG = '01JD0000000000000000000B01';
// useFragmentToken memoises per router key, and every MemoryRouter starts at the same key, so all tests share one token.
const TOKEN = 'tok123';
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });
const me = { userId: 'u-me', email: 'me@x.com', displayName: 'Me', lang: 'en', stage: 'full', personalTenantId: 'P', factors: { pin: true, webauthn: [] }, recoveryCodesLeft: 8 };

let root: Root | undefined;
let host: HTMLElement | undefined;
let calls: { url: string; body: unknown }[] = [];

beforeEach(() => {
  calls = [];
  sessionStorage.clear();
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
});

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = undefined;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

function Where() {
  return <div data-testid="where">{useLocation().pathname}</div>;
}

async function mount(session: 'anon' | 'full', lookup: Record<string, unknown>) {
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (url === '/api/me') return session === 'full' ? json(me) : json({ error: 'unauthorized' }, 401);
    if (url === `/api/invites/${ORG}/lookup`) return json(lookup);
    if (url === `/api/invites/${ORG}/accept`) return json({ tenantId: ORG });
    return json({ error: 'internal' }, 500);
  });
  history.replaceState(null, '', `/invite/${ORG}#token=${TOKEN}`);
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter initialEntries={[`/invite/${ORG}`]}>
          <ToastProvider>
            <SessionProvider>
              <Routes>
                <Route path="/invite/:tenantId" element={<Invite />} />
                <Route path="*" element={<Where />} />
              </Routes>
            </SessionProvider>
          </ToastProvider>
        </MemoryRouter>
      </MantineProvider>,
    );
  });
  await act(async () => { await new Promise((r) => setTimeout(r, 30)); });
}

const button = (label: string) => [...document.querySelectorAll('button')].find((b) => b.textContent === label);
const lookup = { orgName: 'Acme', inviterName: 'Olga', role: 'member', email: 'ann@x.com' };

// Why: a signed-out visitor must come back to this invite after sign-up; the stored token is how.
test('signed out: the invited address and both buttons show, and Sign up stores the invite', async () => {
  await mount('anon', lookup);
  expect(document.body.textContent).toContain('ann@x.com');
  expect(button('Sign in')).toBeTruthy();
  await act(async () => button('Sign up')!.click());
  expect(pendingInvite()).toEqual({ tenantId: ORG, token: TOKEN, email: 'ann@x.com' });
});

// Why: accepting under the wrong account would only fail with a 403; the page says so up front.
test('signed in with another address: the wrong-address text shows and there is no Accept button', async () => {
  await mount('full', { ...lookup, emailMatches: false });
  expect(document.body.textContent).toContain('This invite is for ann@x.com. You are signed in as me@x.com.');
  expect(button('Accept')).toBeUndefined();
});

test('Accept posts the token and lands on the organisation\'s clients', async () => {
  await mount('full', { ...lookup, emailMatches: true });
  await act(async () => button('Accept')!.click());
  await act(async () => { await new Promise((r) => setTimeout(r, 30)); });
  expect(calls.find((c) => c.url === `/api/invites/${ORG}/accept`)?.body).toEqual({ token: TOKEN });
  expect(document.querySelector('[data-testid=where]')!.textContent).toBe(`/t/${ORG}/clients`);
});

// Why: the stored tenant id goes into a path; a tampered value must never be followed.
test('pendingInvite ignores a stored tenant id that is not a ULID', () => {
  sessionStorage.setItem('portal.invite', JSON.stringify({ tenantId: '../x', token: 't', email: 'a@x.com' }));
  expect(pendingInvite()).toBeNull();
});
