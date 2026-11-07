import { fmt } from './i18n';
import type { Strings } from '../strings';

export type AuditEntry = {
  adminId: string; username: string; action: string; target: string; result: string; ip: string;
  /** '' for an entry written before the source was recorded. */
  source: string; detail?: Record<string, string>; at: string;
};

/** A label from a table of them, or the raw key as text when the table has none (a newer backend, an old row). */
export function labelOf(table: Record<string, string>, key: string): string {
  return Object.hasOwn(table, key) ? table[key]! : key;
}

/** null for a success; the failure's code for `failed:<code>`; '' for a result we do not know. */
export function failureOf(result: string): string | null {
  if (result === 'success') return null;
  return result.startsWith('failed:') ? result.slice('failed:'.length) : '';
}

/** The readable name of a failure code: `http_502` is "HTTP 502", an unknown code stays as it is. */
export function failureLabel(code: string, t: Strings): string {
  const http = /^http_(\d{3})$/.exec(code);
  if (http) return fmt(t.audit.httpFailure, { n: http[1]! });
  return code === '' ? '?' : labelOf(t.audit.failures, code);
}

/** "a=1,b=2" as pairs; null when it is not that shape, so the caller shows the raw text. */
export function parsePairs(v: string): [string, string][] | null {
  const out: [string, string][] = [];
  for (const part of v.split(',')) {
    const i = part.indexOf('=');
    if (i <= 0) return null;
    out.push([part.slice(0, i), part.slice(i + 1)]);
  }
  return out.length > 0 ? out : null;
}

const ORDER = ['reason', 'action', 'factor', 'target', 'role', 'name', 'rpId', 'origins', 'fields', 'pinPolicy', 'tokenTtlSec', 'rateLimit', 'revoked'];

/** Detail keys in a reading order: the ones we know first, the rest alphabetically. */
export function detailKeys(detail: Record<string, string> | undefined): string[] {
  const keys = Object.keys(detail ?? {});
  const rank = (k: string) => (ORDER.includes(k) ? ORDER.indexOf(k) : ORDER.length);
  return keys.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

/** The last `n` months as YYYY-MM, newest first, in UTC like the backend's months. */
export function recentMonths(now: Date, n: number): string[] {
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1));
    out.push(`${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`);
  }
  return out;
}

export function monthLabel(month: string, t: Strings): string {
  const [y, m] = month.split('-').map(Number) as [number, number];
  return new Intl.DateTimeFormat(t.locale, { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, 1)));
}
