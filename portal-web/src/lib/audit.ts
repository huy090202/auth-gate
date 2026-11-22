import { fmt } from './i18n';
import type { Strings } from '../strings';

export type AuditEntry = {
  actorId: string; actorName?: string; action: string; target: string; result: string; ip: string;
  detail?: Record<string, string>; at: string;
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
  return labelOf(t.audit.failures, code);
}

const ORDER = ['name', 'rpId', 'origins', 'fields', 'revoked'];

/** Detail keys in a reading order: the ones we know first, the rest alphabetically. */
export function detailKeys(detail: Record<string, string> | undefined): string[] {
  const keys = Object.keys(detail ?? {});
  const rank = (k: string) => (ORDER.includes(k) ? ORDER.indexOf(k) : ORDER.length);
  return keys.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

/** The audit endpoint's URL; the cursor contains '#', so it is always encoded. */
export const auditUrl = (tenantId: string, cursor: string) =>
  `/api/t/${encodeURIComponent(tenantId)}/audit${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`;
