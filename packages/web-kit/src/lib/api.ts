/**
 * The same-origin JSON client both apps use. `codes` is the app's list of backend error codes; it types
 * `isErrorCode`. The failure hook is per client, so each app has its own.
 */
export function createApi<Code extends string>(codes: readonly Code[]) {
  const known = new Set<string>(codes);
  /** True when the backend's code is one this app knows. */
  const isErrorCode = (code: string): code is Code => known.has(code);

  class ApiError extends Error {
    status: number;
    /** An ErrorCode from the backend, or 'network_error', 'invalid_response' or 'unknown' from here. */
    code: string;
    extras: Record<string, unknown>;
    /** The request path, set by api(); lets the failure hook tell a failed /api/me from any other call. */
    path = '';
    constructor(status: number, code: string, extras: Record<string, unknown>) {
      super(`${status} ${code}`);
      this.name = 'ApiError';
      this.status = status;
      this.code = code;
      this.extras = extras;
    }
  }

  let failureHook: ((e: ApiError) => void) | null = null;
  /** The one place that reacts to a failure of any call (session loss, wrong stage). Returns the unsubscribe. */
  function onApiFailure(fn: (e: ApiError) => void): () => void {
    failureHook = fn;
    return () => {
      if (failureHook === fn) failureHook = null;
    };
  }

  /**
   * Same origin only: the browser never talks to AuthGate. Every non-GET is application/json,
   * with or without a body: the backend refuses anything else. An error response that carries
   * Retry-After (429, 503 busy, 409 enrollment_in_progress) gets it as extras.retryAfter, in seconds.
   */
  async function api<T>(
    path: string,
    init: { method?: string; body?: unknown; stepUpToken?: string } = {},
  ): Promise<T> {
    try {
      return await request<T>(path, init);
    } catch (e) {
      if (e instanceof ApiError) {
        e.path = path;
        failureHook?.(e);
      }
      throw e;
    }
  }

  async function request<T>(
    path: string,
    init: { method?: string; body?: unknown; stepUpToken?: string },
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

  return { api, ApiError, onApiFailure, isErrorCode };
}
