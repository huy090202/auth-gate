import { afterEach, expect, test, vi } from 'vitest';
import { createApi } from '../src/lib/api';

const { ApiError, api, isErrorCode } = createApi(['rate_limited', 'busy'] as const);
type ApiError = InstanceType<typeof ApiError>;

afterEach(() => vi.unstubAllGlobals());

function stubFetch(res: Response) {
  const fn = vi.fn<typeof fetch>(async () => res);
  vi.stubGlobal('fetch', fn);
  return fn;
}

test('api sends application/json on a bodyless DELETE and the step-up header when given', async () => {
  const fetchFn = stubFetch(new Response('{}', { status: 200 }));
  await api('/api/clients/c1/keys/previous', { method: 'DELETE', stepUpToken: 'tok-1' });
  const [path, init] = fetchFn.mock.calls[0]!;
  const headers = new Headers(init?.headers);
  expect(path).toBe('/api/clients/c1/keys/previous');
  expect(init?.method).toBe('DELETE');
  expect(init?.body).toBeUndefined();
  expect(headers.get('content-type')).toBe('application/json');
  expect(headers.get('x-step-up-token')).toBe('tok-1');

  await api('/api/me');
  const get = new Headers(fetchFn.mock.calls[1]![1]?.headers);
  expect(fetchFn.mock.calls[1]![1]?.method).toBe('GET');
  expect(get.has('content-type')).toBe(false);
  expect(get.has('x-step-up-token')).toBe(false);
});

test('api maps a 429 to ApiError rate_limited with retryAfter', async () => {
  stubFetch(
    new Response(JSON.stringify({ error: 'rate_limited' }), { status: 429, headers: { 'Retry-After': '7' } }),
  );
  const err = await api('/api/auth/login', { body: { username: 'a' } }).catch((e: unknown) => e);
  expect(err).toBeInstanceOf(ApiError);
  const e = err as ApiError;
  expect(e.status).toBe(429);
  expect(e.code).toBe('rate_limited');
  expect(e.extras.retryAfter).toBe(7);
});

test('isErrorCode tells known codes from the rest', () => {
  expect(isErrorCode('busy')).toBe(true);
  expect(isErrorCode('network_error')).toBe(false);
});
