/** An invite the user opened before they had a full session: kept in this tab so the sign-in flow can come back to it. */
export type PendingInvite = { tenantId: string; token: string; email: string };

const KEY = 'portal.invite';

/** A tenant id is a ULID; anything else never goes into a path. */
export const isUlid = (s: string): boolean => /^[0-9A-HJKMNP-TV-Z]{26}$/.test(s);

export function rememberInvite(v: PendingInvite): void {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(v));
  } catch {
    // Storage can be blocked; the link in the email still works.
  }
}

export function pendingInvite(): PendingInvite | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<PendingInvite> | null;
    if (!v || typeof v.tenantId !== 'string' || typeof v.token !== 'string' || !isUlid(v.tenantId) || v.token === '') return null;
    return { tenantId: v.tenantId, token: v.token, email: typeof v.email === 'string' ? v.email : '' };
  } catch {
    return null;
  }
}

export function forgetInvite(): void {
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // Nothing to clear.
  }
}
