// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { theme } from '@authgate/web-kit/theme';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, expect, test, vi } from 'vitest';
import { SessionGuard, SessionProvider, homeFor } from '../src/session';
import { ToastProvider } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const me = (stage: string) => ({
  userId: 'u1', email: 'a@x.com', displayName: 'A', lang: 'en', stage, personalTenantId: 't1',
  factors: { pin: true, webauthn: false }, recoveryCodesLeft: 10,
});
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });

// Why: a guard that lets a half or enrolling session see a signed-in screen, or sends it to the
// wrong step, is silent in a browser until the backend refuses the first call.
async function guardedAt(stage: string) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal('fetch', async () => json(me(stage)));
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
      <MemoryRouter initialEntries={['/account']}>
        <ToastProvider>
          <SessionProvider>
            <Routes>
              <Route path="/account" element={<SessionGuard>{() => <p id="where">account</p>}</SessionGuard>} />
              <Route path="/sign-in/second-factor" element={<p id="where">second-factor</p>} />
              <Route path="/enroll" element={<p id="where">enroll</p>} />
            </Routes>
          </SessionProvider>
        </ToastProvider>
      </MemoryRouter>
      </MantineProvider>,
    );
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 20));
  });
  const where = host.querySelector('#where')?.textContent;
  await act(async () => root.unmount());
  return where;
}

test('the session guard sends half to the second factor, enroll to enrolment, full through', async () => {
  expect(await guardedAt('half')).toBe('second-factor');
  expect(await guardedAt('enroll')).toBe('enroll');
  expect(await guardedAt('full')).toBe('account');
});

test('homeFor names the screen of each stage', () => {
  expect(homeFor('half')).toBe('/sign-in/second-factor');
  expect(homeFor('enroll')).toBe('/enroll');
  expect(homeFor('full')).toBe('/account');
  expect(homeFor('anon')).toBe('/sign-in');
});
