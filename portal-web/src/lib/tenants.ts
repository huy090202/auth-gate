import { api } from './api';
import type { Strings } from '../strings';

export type Role = 'owner' | 'admin' | 'member';
export type Tenant = { tenantId: string; kind: 'personal' | 'organisation'; name: string; role: Role };

export async function fetchTenants(): Promise<Tenant[]> {
  const r = await api<{ tenants?: Tenant[] }>('/api/tenants');
  return r.tenants ?? [];
}

/** The workspace's name as people see it: the personal one has a fixed, translated name. */
export const tenantLabel = (t: Tenant, s: Strings): string => (t.kind === 'personal' ? s.shell.personal : t.name);

/** The ids that go into a URL: a tenant id is a ULID, but nothing is trusted to stay inside a path segment. */
export const clientsPath = (tenantId: string) => `/t/${encodeURIComponent(tenantId)}/clients`;
export const auditPath = (tenantId: string) => `/t/${encodeURIComponent(tenantId)}/audit`;
export const clientPath = (tenantId: string, clientId: string) => `${clientsPath(tenantId)}/${encodeURIComponent(clientId)}`;
