import { afterEach, expect, test, vi } from 'vitest';
import { AVATAR_MAX_BYTES, avatarForm, checkAvatar, uploadAvatar } from '../src/lib/avatar';

afterEach(() => vi.unstubAllGlobals());

// Why: a file the presigned form would refuse (S3 answers an XML error the user cannot read) must
// be caught before any request, with a sentence.
test('checkAvatar refuses another type and a file over 1 MiB', () => {
  expect(checkAvatar({ type: 'image/png', size: 10 })).toBeNull();
  expect(checkAvatar({ type: 'image/jpeg', size: AVATAR_MAX_BYTES })).toBeNull();
  expect(checkAvatar({ type: 'image/webp', size: 1 })).toBeNull();
  expect(checkAvatar({ type: 'image/gif', size: 10 })).toBe('type');
  expect(checkAvatar({ type: 'image/svg+xml', size: 10 })).toBe('type');
  expect(checkAvatar({ type: 'image/png', size: AVATAR_MAX_BYTES + 1 })).toBe('size');
  expect(checkAvatar({ type: 'image/png', size: 0 })).toBe('size');
});

// Why: S3 ignores every form field after `file`; a policy field appended after it would be dropped
// and the upload refused.
test('the upload form carries every presigned field, then the file last', () => {
  const form = avatarForm({ key: 'avatars/u/x.png', 'Content-Type': 'image/png', policy: 'p' }, new Blob(['x']));
  expect([...form.keys()]).toEqual(['key', 'Content-Type', 'policy', 'file']);
});

test('an invalid file is refused before a presign is requested', async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
  await expect(uploadAvatar(new File(['x'], 'a.gif', { type: 'image/gif' }))).rejects.toMatchObject({ code: 'invalid_avatar' });
  expect(fetchMock).not.toHaveBeenCalled();
});

test('upload posts the form to the presigned url without credentials, then confirms the key', async () => {
  const seen: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    seen.push({ url, init });
    if (url === '/api/me/avatar') return new Response(JSON.stringify({ url: 'https://b.s3.eu-west-1.amazonaws.com/', fields: { key: 'avatars/u/x.png' }, key: 'avatars/u/x.png' }), { status: 200 });
    if (url.startsWith('https://b.s3')) return new Response(null, { status: 204 });
    return new Response(JSON.stringify({ avatarUrl: '/avatars/u/x.png' }), { status: 200 });
  });
  const url = await uploadAvatar(new File(['x'], 'a.png', { type: 'image/png' }));
  expect(url).toBe('/avatars/u/x.png');
  expect(seen.map((s) => s.url)).toEqual(['/api/me/avatar', 'https://b.s3.eu-west-1.amazonaws.com/', '/api/me/avatar/confirm']);
  expect(seen[1]!.init!.credentials).toBe('omit');
  expect(JSON.parse(seen[2]!.init!.body as string)).toEqual({ key: 'avatars/u/x.png' });
});

test('a refused upload does not confirm', async () => {
  const seen: string[] = [];
  vi.stubGlobal('fetch', async (url: string) => {
    seen.push(url);
    if (url === '/api/me/avatar') return new Response(JSON.stringify({ url: 'https://b.s3.amazonaws.com/', fields: {}, key: 'k' }), { status: 200 });
    return new Response('<Error/>', { status: 403 });
  });
  await expect(uploadAvatar(new File(['x'], 'a.png', { type: 'image/png' }))).rejects.toMatchObject({ code: 'upload_failed' });
  expect(seen).not.toContain('/api/me/avatar/confirm');
});
