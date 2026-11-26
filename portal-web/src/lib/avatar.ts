import { ApiError, api } from './api';

/** The most the backend accepts (internal/platform/avatar MaxBytes) and the types it takes. */
export const AVATAR_MAX_BYTES = 1 << 20;
export const AVATAR_TYPES = ['image/png', 'image/jpeg', 'image/webp'] as const;

export type AvatarProblem = 'type' | 'size' | null;

/** Checked before a presign is asked for, so a file the form would refuse is never sent. */
export function checkAvatar(file: { type: string; size: number }): AvatarProblem {
  if (!(AVATAR_TYPES as readonly string[]).includes(file.type)) return 'type';
  if (file.size < 1 || file.size > AVATAR_MAX_BYTES) return 'size';
  return null;
}

type Presigned = { url: string; fields: Record<string, string>; key: string };

/**
 * The presigned form of S3: every field the Portal returned, then the file last (S3 ignores anything
 * after it). Posted without credentials: the target is S3, or the dev route on the site's own origin.
 */
export function avatarForm(fields: Record<string, string>, file: Blob): FormData {
  const form = new FormData();
  for (const [k, v] of Object.entries(fields)) form.append(k, v);
  form.append('file', file);
  return form;
}

/** Upload then confirm. Resolves with the new avatar's URL. A refused upload throws ApiError(…, 'upload_failed'). */
export async function uploadAvatar(file: File): Promise<string> {
  const problem = checkAvatar(file);
  if (problem) throw new ApiError(400, 'invalid_avatar', {});
  const p = await api<Presigned>('/api/me/avatar', { body: { contentType: file.type } });
  let res: Response;
  try {
    res = await fetch(p.url, { method: 'POST', body: avatarForm(p.fields, file), credentials: 'omit' });
  } catch {
    throw new ApiError(0, 'upload_failed', {});
  }
  if (!res.ok) throw new ApiError(res.status, 'upload_failed', {});
  const r = await api<{ avatarUrl: string }>('/api/me/avatar/confirm', { body: { key: p.key } });
  return r.avatarUrl;
}
