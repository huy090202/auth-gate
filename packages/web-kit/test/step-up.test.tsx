// @vitest-environment jsdom
import { MantineProvider } from '@mantine/core';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, expect, test, vi } from 'vitest';
import { useStepUp, type StepUpConfig } from '../src/step-up';
import { theme } from '../src/theme';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

// Why: a root left mounted keeps the modal's close transition scheduled; it then runs after the
// file's jsdom is torn down and fails the run with "window is not defined" (seen in CI).
let root: Root | undefined;
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

// A factor form that hands over a token at once, so the test drives only the hook's own logic.
const cfg: StepUpConfig = {
  strings: { title: 'Confirm', intro: 'intro', noFactor: 'none', loading: 'loading' },
  explain: () => 'err',
  api: () => Promise.reject(new Error('unused')),
  loadFactors: () => Promise.resolve({ pin: true, webauthn: false }),
  startPath: '/s',
  verifyPath: '/v',
  renderFactorForm: (p) => <button type="button" onClick={() => p.onDone({ stepUpToken: 'tok' })}>give token</button>,
};

let api: ReturnType<typeof useStepUp<'a'>>;
function Probe() {
  api = useStepUp<'a'>(cfg);
  return <>{api.modal}</>;
}

async function mount() {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false }));
  root = createRoot(document.body.appendChild(document.createElement('div')));
  await act(async () => root!.render(<MantineProvider theme={theme}><Probe /></MantineProvider>));
}
const give = async () => {
  let b: HTMLButtonElement | undefined;
  for (let i = 0; i < 50 && !b; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
    b = [...document.querySelectorAll('button')].find((x) => x.textContent === 'give token');
  }
  await act(async () => b!.click());
};

test('a token refused with step_up_required opens the modal once more; the second token reaches run', async () => {
  await mount();
  const run = vi.fn().mockRejectedValueOnce({ code: 'step_up_required' }).mockResolvedValueOnce('done');
  let result: Promise<string | undefined>;
  await act(async () => { result = api.stepUp('a', run); });
  await give();
  await give();
  expect(await result!).toBe('done');
  expect(run).toHaveBeenCalledTimes(2);
});

test('any other error reaches the caller, and closing the modal resolves undefined', async () => {
  await mount();
  let failed: Promise<unknown>;
  await act(async () => { failed = api.stepUp('a', () => Promise.reject({ code: 'busy' })).catch((e) => e); });
  await give();
  expect(await failed!).toEqual({ code: 'busy' });

  let closed: Promise<string | undefined>;
  await act(async () => { closed = api.stepUp('a', async () => 'x'); });
  await act(async () => document.querySelector<HTMLButtonElement>('.mantine-Modal-close')!.click());
  expect(await closed!).toBeUndefined();
});
