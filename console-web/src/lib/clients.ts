import { api } from './api';

export type ClientSummary = { clientId: string; name: string; rpId: string; createdAt: string; disabledAt?: string };
export type ClientView = {
  clientId: string; name: string; rpId: string; origins: string[];
  pinPolicy: { length: number; maxAttempts: number; lockoutSec: number };
  tokenTtlSec: number; rateLimit: { burst: number; perSecond: number };
  createdAt: string; disabledAt?: string;
  /** Set by the Console's backend on the Console's own client, which is read-only here. */
  ownClient?: boolean;
};

/** AuthGate lists by cursor only, and smoke clients crowd the list: read every page, at most this many. */
export const MAX_PAGES = 50;

export async function fetchAllClients(): Promise<{ clients: ClientSummary[]; capped: boolean }> {
  const clients: ClientSummary[] = [];
  let cursor = '';
  for (let page = 0; page < MAX_PAGES; page++) {
    const r = await api<{ clients?: ClientSummary[]; nextCursor?: string }>(`/api/clients${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`);
    clients.push(...(r.clients ?? []));
    if (!r.nextCursor) return { clients, capped: false };
    cursor = r.nextCursor;
  }
  return { clients, capped: true };
}

/** A whole number from a text field, or null. */
export function parseWhole(v: string): number | null {
  return /^\d{1,9}$/.test(v.trim()) ? Number(v.trim()) : null;
}

/** A positive decimal from a text field (plain or exponent form, as a stored value stringifies), or null. */
export function parsePositive(v: string): number | null {
  const s = v.trim();
  if (!/^(\d+(\.\d+)?|\.\d+)([eE][+-]?\d+)?$/.test(s)) return null;
  const n = Number(s);
  return Number.isFinite(n) && n > 0 ? n : null;
}

/** The ranges AuthGate enforces; the form mirrors them so a bad value is caught before a step-up is spent. */
export const RANGES = {
  pinLength: [4, 8], pinMaxAttempts: [1, 10], pinLockout: [1, 86400], tokenTtl: [1, 900], rateBurst: [1, 10000],
} as const;
export const MAX_PER_SECOND = 5000;

/** The origins typed one per line. */
export const splitOrigins = (v: string) => v.split('\n').map((l) => l.trim()).filter(Boolean);

/** The subject-id rule the backend applies (1 to 256 bytes, nothing that could change a path), so a bad id is caught before a step-up is spent. */
export function validSubject(v: string): boolean {
  if (v === '' || v === '.' || v === '..' || new TextEncoder().encode(v).length > 256) return false;
  return !/[/?#%\u0000-\u001f\u007f]/.test(v);
}
