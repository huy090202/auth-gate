// Every code the Console backend can send: its own, and the AuthGate ones it passes through.
export const errorCodes = [
  'unauthorized', 'invalid_credentials', 'second_factor_required', 'enrollment_required', 'already_verified',
  'forbidden', 'step_up_required', 'invalid_action', 'rate_limited', 'unsupported_media_type', 'not_found',
  'invalid_json', 'invalid_username', 'invalid_password', 'wrong_password', 'username_taken', 'last_factor',
  'factor_not_enrolled', 'cannot_modify_self', 'last_master', 'own_client_protected', 'invalid_month',
  'authgate_unavailable', 'internal', 'verification_failed', 'locked', 'challenge_gone', 'wrong_factor',
  'pin_rejected', 'registration_failed', 'invalid_client', 'invalid_cursor', 'invalid_id', 'client_not_found',
  'rotate_conflict', 'invalid_token', 'invalid_factor', 'enrollment_in_progress', 'invalid_role', 'busy',
] as const;
export type ErrorCode = (typeof errorCodes)[number];

export class ApiError extends Error {
  status: number;
  /** An ErrorCode from the backend, or 'network_error', 'invalid_response' or 'unknown' from here. */
  code: string;
  extras: Record<string, unknown>;
  constructor(status: number, code: string, extras: Record<string, unknown>) {
    super(`${status} ${code}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.extras = extras;
  }
}

/**
 * Same origin only: the browser never talks to AuthGate. Every non-GET is application/json,
 * with or without a body: the backend refuses anything else. An error response that carries
 * Retry-After (429, 503 busy, 409 enrollment_in_progress) gets it as extras.retryAfter, in seconds.
 */
export async function api<T>(
  path: string,
  init: { method?: string; body?: unknown; stepUpToken?: string } = {},
): Promise<T> {
  const method = init.method ?? (init.body === undefined ? 'GET' : 'POST');
  const headers: Record<string, string> = {};
  if (method !== 'GET') headers['content-type'] = 'application/json';
  if (init.stepUpToken) headers['x-step-up-token'] = init.stepUpToken;
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers,
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
    });
  } catch {
    throw new ApiError(0, 'network_error', {});
  }
  const text = await res.text().catch(() => '');
  let body: Record<string, unknown> = {};
  let parsed = true;
  try {
    const v: unknown = text ? JSON.parse(text) : {};
    if (v !== null && typeof v === 'object' && !Array.isArray(v)) body = v as Record<string, unknown>;
    else parsed = false;
  } catch {
    parsed = false;
  }
  // A 2xx we cannot read is a failure, not an empty success.
  if (res.ok && !parsed) throw new ApiError(res.status, 'invalid_response', {});
  if (!res.ok) {
    const { error, ...extras } = body;
    const wait = Number.parseInt(res.headers.get('retry-after') ?? '', 10);
    if (Number.isFinite(wait) && wait >= 0) extras.retryAfter = wait;
    throw new ApiError(res.status, typeof error === 'string' ? error : 'unknown', extras);
  }
  return body as T;
}
