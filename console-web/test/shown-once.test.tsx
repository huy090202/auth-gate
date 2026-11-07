// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { ShownOnce } from '../src/ShownOnce';
import { theme } from '../src/theme';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

// Why this test exists: ShownOnce guards a value that can never be read again. If Escape, a click
// on the backdrop or an early Close dismissed it, the key would be lost with no error and no way
// to notice in a browser run until someone needs the key. Nothing else would catch that.

let root: Root;
let host: HTMLElement;
const onClose = vi.fn();

beforeEach(async () => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
  onClose.mockClear();
  host = document.createElement('div');
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} defaultColorScheme="light" theme={theme}>
        <ShownOnce title="Client created" label="Access key" value="key-123" onClose={onClose} />
      </MantineProvider>,
    );
  });
});

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const closeButton = () => [...document.querySelectorAll('button')].find((b) => b.textContent === 'Close')!;

test('shows the value, no close icon, and Close is off until the box is ticked', () => {
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  expect(document.body.textContent).toContain('key-123');
  expect(document.querySelector('.mantine-Modal-close')).toBeNull();
  expect(closeButton().disabled).toBe(true);
});

test('Escape, a click on the backdrop and Close before ticking leave it open', async () => {
  await act(async () => {
    document.querySelector('[role="dialog"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  });
  await act(async () => {
    document.querySelector<HTMLElement>('.mantine-Modal-overlay')!.click();
  });
  await act(async () => {
    closeButton().click();
  });
  expect(onClose).not.toHaveBeenCalled();
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
});

test('ticking "saved" then Close calls onClose once', async () => {
  const box = document.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
  await act(async () => {
    box.click();
  });
  expect(closeButton().disabled).toBe(false);
  await act(async () => {
    closeButton().click();
  });
  expect(onClose).toHaveBeenCalledTimes(1);
});
