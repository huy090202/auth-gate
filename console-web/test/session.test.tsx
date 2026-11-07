// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, expect, test, vi } from 'vitest';
import { api } from '../src/lib/api';
import { SessionProvider, useMe } from '../src/session';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

// Why: both cases are silent in a browser run. A /me refusal that is not `unauthorized` used to leave
// the app on a spinner or refetch /me in a loop, and a navigation that ran before the refresh made the
// target screen see the old stage and bounce back. Nothing throws and nothing is logged.

afterEach(() => {
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const me = (stage: string) => ({ username: 'ann', role: 'master', stage, mustChangePassword: false, factors: { pin: true, webauthn: [] } });
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'content-type': 'application/json' } });

async function mount(ui: React.ReactNode) {
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => {
    root.render(<MemoryRouter><SessionProvider>{ui}</SessionProvider></MemoryRouter>);
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 20));
  });
  return { host, root };
}

function Status() {
  const { status } = useMe();
  return <p id="status">{status}</p>;
}

test('a /me refusal other than unauthorized ends in the error state, with one /me call', async () => {
  const calls: string[] = [];
  vi.stubGlobal('fetch', async (url: string) => {
    calls.push(url);
    return json({ error: 'second_factor_required' }, 401);
  });
  const { host, root } = await mount(<Status />);
  expect(host.querySelector('#status')?.textContent).toBe('error');
  expect(calls).toEqual(['/api/me']);
  await act(async () => root.unmount());
});

test('the stage refresh finishes before the failure hook navigates', async () => {
  let meCalls = 0;
  vi.stubGlobal('fetch', async (url: string) => {
    if (url === '/api/me') return json(me(++meCalls === 1 ? 'full' : 'half'));
    return json({ error: 'second_factor_required' }, 401);
  });
  const seen: string[] = [];
  function Target() {
    seen.push(useMe().status);
    return <p id="target">second factor</p>;
  }
  function Trigger() {
    return <button onClick={() => void api('/api/clients').catch(() => {})}>go</button>;
  }
  const { host, root } = await mount(
    <Routes>
      <Route path="/" element={<Trigger />} />
      <Route path="/sign-in/second-factor" element={<Target />} />
    </Routes>,
  );
  await act(async () => {
    host.querySelector('button')!.click();
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 20));
  });
  expect(host.querySelector('#target')).not.toBeNull();
  expect(seen[0]).toBe('half'); // the first render of the target already sees the new stage
  await act(async () => root.unmount());
});
