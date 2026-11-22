import { api } from './api';

/** `owner` is the tenant id that owns the client; absent for an operator's. */
export type ClientSummary = { clientId: string; name: string; rpId: string; createdAt: string; disabledAt?: string; owner?: string };
export type ClientView = {
  clientId: string; name: string; rpId: string; origins: string[];
  pinPolicy: { length: number; maxAttempts: number; lockoutSec: number };
  tokenTtlSec: number; rateLimit: { burst: number; perSecond: number };
  createdAt: string; disabledAt?: string;
  owner?: string;
  /** Who disabled the client; absent unless it is disabled. */
  disabledBy?: string;
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

export { MAX_PER_SECOND, RANGES, parsePositive, parseWhole, splitOrigins, validSubject } from '@authgate/web-kit/clients';
