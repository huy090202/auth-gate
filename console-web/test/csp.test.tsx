// @vitest-environment jsdom
import { ActionIcon, Button, MantineProvider, Modal, SegmentedControl } from '@mantine/core';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter } from 'react-router';
import { afterEach, expect, test, vi } from 'vitest';
import { SchemeToggle } from '../src/App';
import { mantineVarsCss } from '../src/lib/mantine-vars';
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
  expect(host.querySelector('[aria-expanded]')?.getAttribute('aria-expanded')).toBe(String(!collapsed));
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});
