// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, test, vi } from 'vitest';
import { mantineVarsCss } from '@authgate/web-kit/mantine-vars';
import { theme } from '@authgate/web-kit/theme';
import { RecoveryCodes } from '../src/screens/RecoveryCodes';
import { SignUp } from '../src/screens/SignUp';
import { SessionProvider } from '../src/session';
import { ToastProvider } from '../src/toast';

const varsFile = join(import.meta.dirname, '../src/mantine-vars.css');

test('mantine-vars.css matches mantineVarsCss()', () => {
  // `pnpm --filter portal-web gen:vars` sets GEN_VARS and rewrites the file.
  if (process.env.GEN_VARS) writeFileSync(varsFile, mantineVarsCss());
  expect(readFileSync(varsFile, 'utf8')).toBe(mantineVarsCss());
});

test('index.html has no inline script, and Turnstile is not loaded by the page itself', () => {
  const html = readFileSync(join(import.meta.dirname, '../index.html'), 'utf8');
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)];
  expect(scripts.length).toBeGreaterThanOrEqual(2);
  for (const [, attrs, body] of scripts) {
    expect(attrs).toMatch(/\bsrc="/);
    expect(body!.trim()).toBe('');
  }
  expect(html).not.toContain('challenges.cloudflare.com');
  expect(html).not.toMatch(/\son[a-z]+=|style=/i);
});

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  document.body.innerHTML = '';
  document.head.querySelectorAll('script').forEach((s) => s.remove());
  vi.unstubAllGlobals();
});

async function render(ui: React.ReactNode) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal('fetch', async (url: string) =>
    url === '/api/config'
      ? new Response('{"turnstileSiteKey":"1x00000000000000000000AA"}', { status: 200 })
      : new Response('{"error":"unauthorized"}', { status: 401 }));
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <MemoryRouter>
          <ToastProvider>
            <SessionProvider>{ui}</SessionProvider>
          </ToastProvider>
        </MemoryRouter>
      </MantineProvider>,
    );
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 30));
  });
  return { host, root };
}

// Why: a Mantine part that injects a <style> is silent in a browser until the production CSP
// refuses it. These render the screens with form controls, a checkbox and a password field.
test('the sign-up screen renders no <style> element and adds the Turnstile script only when it mounts', async () => {
  expect(document.querySelector('script[src*="challenges.cloudflare.com"]')).toBeNull();
  const { host, root } = await render(<SignUp />);
  expect(host.querySelector('h1')).not.toBeNull();
  expect(host.querySelectorAll('input').length).toBe(3);
  expect(document.querySelectorAll('style').length).toBe(0);
  expect(document.querySelector('script[src="https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit"]')).not.toBeNull();
  await act(async () => root.unmount());
});

test('the recovery codes screen lists ten codes and keeps Continue off until they are saved', async () => {
  const codes = Array.from({ length: 10 }, (_, i) => `ABCD${i}-EFGH${i}`);
  let went = 0;
  const { host, root } = await render(<RecoveryCodes codes={codes} onContinue={() => went++} />);
  expect(host.querySelectorAll('.code-grid li').length).toBe(10);
  const next = [...host.querySelectorAll('button')].find((b) => b.textContent === 'Continue')!;
  expect(next.disabled).toBe(true);
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  expect(next.disabled).toBe(false);
  await act(async () => next.click());
  expect(went).toBe(1);
  expect(document.querySelectorAll('style').length).toBe(0);
  expect(JSON.stringify(Object.entries(localStorage))).not.toContain('ABCD0');
  await act(async () => root.unmount());
});
