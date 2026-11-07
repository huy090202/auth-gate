// @vitest-environment jsdom
import { Button, MantineProvider, Modal } from '@mantine/core';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, expect, test, vi } from 'vitest';
import { mantineVarsCss } from '../src/lib/mantine-vars';
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
