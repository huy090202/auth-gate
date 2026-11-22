// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { TypeToConfirm } from '../src/ui';
import { theme } from '../src/theme';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

// Why this test exists: the delete confirmation is the only thing between a click and a client's
// keys stopping for good; a comparison that ignored case or spaces would let a near miss through.

let root: Root;
let host: HTMLElement;
const onConfirm = vi.fn();

beforeEach(async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  onConfirm.mockClear();
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <TypeToConfirm title="Delete" body="Gone." expected="My App" confirmLabel="Delete it" cancelLabel="Cancel" typeLabel="Type {expected} to confirm" onConfirm={onConfirm} onCancel={() => {}} />
      </MantineProvider>,
    );
  });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = '';
});

async function type(value: string) {
  const input = document.querySelector('input') as HTMLInputElement;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!;
  await act(async () => {
    set.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
}

const confirm = () => [...document.querySelectorAll('button')].find((b) => b.textContent === 'Delete it') as HTMLButtonElement;

test('the confirm button enables only on an exact match', async () => {
  expect(confirm().disabled).toBe(true);
  for (const near of ['my app', 'My App ', ' My App', 'My  App', 'My Ap']) {
    await type(near);
    expect(confirm().disabled, near).toBe(true);
  }
  await type('My App');
  expect(confirm().disabled).toBe(false);
  await act(async () => confirm().click());
  expect(onConfirm).toHaveBeenCalledTimes(1);
});
