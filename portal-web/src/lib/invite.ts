/**
 * An invite the user opened before they had a full session, kept so the sign-in flow can come back
 * to it. It lives in localStorage because the verification email opens a new tab, and for a day only.
 */
export type PendingInvite = { tenantId: string; token: string; email: string };

const KEY = 'portal.invite';
const MAX_AGE_MS = 24 * 60 * 60 * 1000;

/** A tenant id is a ULID; anything else never goes into a path. */
export const isUlid = (s: string): boolean => /^[0-9A-HJKMNP-TV-Z]{26}$/.test(s);

export function rememberInvite(v: PendingInvite): void {
  try {
    localStorage.setItem(KEY, JSON.stringify({ ...v, savedAt: Date.now() }));
  } catch {
    // Storage can be blocked; the link in the email still works.
  }
}

export function pendingInvite(): PendingInvite | null {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<PendingInvite> & { savedAt?: unknown } | null;
    const fresh = !!v && typeof v.savedAt === 'number' && Date.now() - v.savedAt <= MAX_AGE_MS;
    if (!v || !fresh || typeof v.tenantId !== 'string' || typeof v.token !== 'string' || !isUlid(v.tenantId) || v.token === '') {
      forgetInvite();
      return null;
    }
    return { tenantId: v.tenantId, token: v.token, email: typeof v.email === 'string' ? v.email : '' };
  } catch {
    return null;
  }
}

export function forgetInvite(): void {
  try {
    localStorage.removeItem(KEY);
  } catch {
    // Nothing to clear.
  }
}
