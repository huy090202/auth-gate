// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, expect, test, vi } from 'vitest';
import { theme } from '../src/theme';
import { ToastProvider, useToast } from '../src/toast';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function stubMatchMedia() {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {},
    addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
  vi.useRealTimers();
});

function Trigger() {
  const toast = useToast();
  return <button type="button" id="go" onClick={() => toast('Changes saved.')}>go</button>;
}

// Why: a success note was easy to miss. The toast must show, go away by itself (but not while the
// pointer is on it), and, like everything else here, render no <style> element under the strict CSP.
test('a toast shows, waits while hovered, auto-dismisses and renders no <style>', async () => {
  stubMatchMedia();
  vi.useFakeTimers();
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(
      <MantineProvider withCssVariables={false} withGlobalClasses={false} theme={theme}>
        <ToastProvider><Trigger /></ToastProvider>
      </MantineProvider>,
    );
  });
  const live = host.querySelector('[role="status"]')!;
  expect(live.getAttribute('aria-live')).toBe('polite');
  await act(async () => host.querySelector<HTMLButtonElement>('#go')!.click());
  expect(live.textContent).toContain('Changes saved.');
  expect(document.querySelectorAll('style').length).toBe(0);

  // Hovered: still there well past 4 s.
  const item = live.querySelector('.toast')!;
  await act(async () => {
    item.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, relatedTarget: document.body }));
  });
  await act(async () => vi.advanceTimersByTime(6000));
  expect(live.textContent).toContain('Changes saved.');

  // Left: gone after about 4 s (plus the exit animation).
  await act(async () => {
    item.dispatchEvent(new MouseEvent('mouseout', { bubbles: true, relatedTarget: document.body }));
  });
  await act(async () => vi.advanceTimersByTime(3900));
  expect(live.textContent).toContain('Changes saved.');
  await act(async () => vi.advanceTimersByTime(200)); // 4 s: it starts to leave
  expect(live.querySelector('.toast')?.getAttribute('data-leaving')).toBe('true');
  await act(async () => vi.advanceTimersByTime(300)); // and is removed after the exit animation
  expect(live.textContent).toBe('');
  expect(document.querySelectorAll('style').length).toBe(0);
  await act(async () => root.unmount());
});

test('the close button dismisses a toast', async () => {
  stubMatchMedia();
  vi.useFakeTimers();
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(<MantineProvider theme={theme}><ToastProvider><Trigger /></ToastProvider></MantineProvider>);
  });
  await act(async () => host.querySelector<HTMLButtonElement>('#go')!.click());
  await act(async () => host.querySelector<HTMLButtonElement>('.toast-close')!.click());
  await act(async () => vi.advanceTimersByTime(300));
  expect(host.querySelector('.toast')).toBeNull();
  await act(async () => root.unmount());
});

// Why: copying an id chip repeatedly stacked identical "Copied" toasts, and a toast of the previous
// user could outlive a sign-out onto the sign-in page. Both are visible only in a browser.
function Many() {
  const toast = useToast();
  return (
    <>
      <button type="button" id="dup" onClick={() => { toast('Copied'); toast('Copied'); }}>dup</button>
      <button type="button" id="many" onClick={() => ['a', 'b', 'c', 'd', 'e'].forEach((m) => toast(m))}>many</button>
      <button type="button" id="clear" onClick={() => toast.clear()}>clear</button>
    </>
  );
}

test('identical toasts are dropped, the stack holds three, and clear() empties it', async () => {
  stubMatchMedia();
  vi.useFakeTimers();
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(<MantineProvider theme={theme}><ToastProvider><Many /></ToastProvider></MantineProvider>);
  });
  const click = (id: string) => act(async () => host.querySelector<HTMLButtonElement>(id)!.click());
  await click('#dup');
  await click('#dup');
  expect(host.querySelectorAll('.toast').length).toBe(1);
  await click('#many');
  expect([...host.querySelectorAll('.toast-text')].map((e) => e.textContent)).toEqual(['c', 'd', 'e']);
  await click('#clear');
  expect(host.querySelector('.toast')).toBeNull();
  await act(async () => root.unmount());
});
