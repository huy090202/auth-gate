// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, expect, test, vi } from 'vitest';
import { NavItem, ShellLayout } from '../src/shell-layout';
import { theme } from '../src/theme';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const strings = { mainNav: 'Main', expandSidebar: 'Expand', collapseSidebar: 'Collapse the sidebar', collapse: 'Collapse', openMenu: 'Open menu', closeMenu: 'Close menu' };

// Why: a root left mounted keeps the drawer's close transition scheduled past the jsdom teardown.
let root: Root | undefined;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

let wide = false;
let listeners: Array<(e: { matches: boolean }) => void> = [];

function Shell({ routeKey }: { routeKey: string }) {
  return (
    <ShellLayout
      storageKey="t" strings={strings} brand={<b>Brand</b>} collapseIcon={<i />} menuIcon={<i />} routeKey={routeKey}
      nav={<><NavItem component="a" to="/one" label="One" icon={null} /><NavItem component="a" to="/two" label="Two" icon={null} /></>}
      account={{ avatar: <span />, name: 'Ann', buttonLabel: 'Account', menu: null }}
    >
      body
    </ShellLayout>
  );
}

async function mount(routeKey = '/one') {
  wide = false;
  listeners = [];
  vi.stubGlobal('matchMedia', (q: string) => ({
    get matches() { return q === '(min-width: 769px)' ? wide : false; },
    media: q,
    addEventListener: (_: string, f: (e: { matches: boolean }) => void) => listeners.push(f),
    removeEventListener: (_: string, f: (e: { matches: boolean }) => void) => { listeners = listeners.filter((x) => x !== f); },
    addListener() {}, removeListener() {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  root = createRoot(document.body.appendChild(document.createElement('div')));
  const render = (k: string) => act(async () => root!.render(<MantineProvider theme={theme} withCssVariables={false} withGlobalClasses={false}><Shell routeKey={k} /></MantineProvider>));
  await render(routeKey);
  return render;
}
const menu = () => document.querySelector<HTMLButtonElement>('button[aria-label="Open menu"]')!;
const dialog = () => document.querySelector('[role="dialog"]');
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 400)); });
const open = async () => { await act(async () => menu().click()); await settle(); };

// Why: the drawer is the only way to reach navigation on a phone; it must open, and must close after a choice.
test('the menu button opens the drawer with the navigation, and a link closes it', async () => {
  await mount();
  expect(menu().getAttribute('aria-expanded')).toBe('false');
  await open();
  expect(menu().getAttribute('aria-expanded')).toBe('true');
  expect(dialog()!.querySelectorAll('a').length).toBe(2);
  await act(async () => dialog()!.querySelector<HTMLAnchorElement>('a')!.click());
  await settle();
  expect(dialog()).toBeNull();
  expect(menu().getAttribute('aria-expanded')).toBe('false');
});

// Why: the phone's Back button changes the route without touching the drawer.
test('a new routeKey closes the open drawer', async () => {
  const render = await mount('/one');
  await open();
  expect(dialog()).not.toBeNull();
  await render('/two');
  await settle();
  expect(dialog()).toBeNull();
});

// Why: turning a tablet to landscape crosses 768 px; a modal drawer must not stay over the wide layout.
test('the drawer closes when the screen becomes wide', async () => {
  await mount();
  await open();
  expect(dialog()).not.toBeNull();
  wide = true;
  await act(async () => listeners.forEach((f) => f({ matches: true })));
  await settle();
  expect(dialog()).toBeNull();
});

// Why: CSP style-src 'self' refuses injected <style>; silent until production.
test('opening the drawer injects no <style>', async () => {
  await mount();
  await open();
  expect(document.querySelectorAll('style').length).toBe(0);
});
