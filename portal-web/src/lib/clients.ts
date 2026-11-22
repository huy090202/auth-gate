import { api } from './api';

export { RANGES, parseWhole, splitOrigins, validSubject } from '@authgate/web-kit/clients';

export type ClientSummary = { clientId: string; name: string; rpId: string; createdAt: string; disabledAt?: string };
export type ClientView = {
  clientId: string; name: string; rpId: string; origins: string[];
  pinPolicy: { length: number; maxAttempts: number; lockoutSec: number };
  tokenTtlSec: number;
  createdAt: string; disabledAt?: string;
  /** Who disabled the client; absent unless it is disabled. `operator` cannot be undone from the Portal. */
  disabledBy?: string;
};

/** True for a client an administrator disabled: the Portal shows it as locked and offers no Enable. */
export const lockedByOperator = (c: Pick<ClientView, 'disabledBy'>): boolean => c.disabledBy === 'operator';
export const isDisabled = (c: Pick<ClientView, 'disabledAt'>): boolean => c.disabledAt !== undefined && c.disabledAt !== '';

/** A workspace holds a handful of clients (the quota is small), but AuthGate pages by cursor: read at most this many pages. */
export const MAX_PAGES = 50;

export async function fetchAllClients(tenantId: string): Promise<{ clients: ClientSummary[]; capped: boolean }> {
  const clients: ClientSummary[] = [];
  let cursor = '';
  for (let page = 0; page < MAX_PAGES; page++) {
    const r = await api<{ clients?: ClientSummary[]; nextCursor?: string }>(
      `/api/t/${encodeURIComponent(tenantId)}/clients${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`,
    );
    clients.push(...(r.clients ?? []));
    if (!r.nextCursor) return { clients, capped: false };
    cursor = r.nextCursor;
  }
  return { clients, capped: true };
}

/** Every request about one client of one workspace goes under this path. */
export const clientApi = (tenantId: string, clientId: string) => `/api/t/${encodeURIComponent(tenantId)}/clients/${encodeURIComponent(clientId)}`;
