import type { ClientSummary } from './clients';

/** One row of GET /api/tenants: the Portal's tenant, with AuthGate's counters when they could be read. */
export type TenantRow = {
  tenantId: string; name: string; kind: 'org' | 'personal' | string; createdAt: string; memberCount: number; creatorEmail: string;
  suspendedAt?: string; deletingAt?: string; clientCount?: number; clientQuota?: number;
};
export type Member = { userId: string; displayName: string; email: string; role: string; joinedAt: string };
/** AuthGate's side of a tenant. */
export type Owner = { tenantId: string; clientCount: number; clientQuota: number; suspendedAt?: string };
/** Who last suspended, from the Console audit; only meaningful while the thing is suspended. */
export type LastSuspension = { reason: string; username: string; at: string };
export type TenantView = {
  tenant: TenantRow; members: Member[]; pendingInvites: number; owner: Owner;
  clients: { clients?: ClientSummary[]; nextCursor?: string }; lastSuspension?: LastSuspension;
};
export type TenantAuditEntry = {
  actorId: string; actorName?: string; action: string; target: string; result: string; ip: string; detail?: Record<string, string>; at: string;
};
export type UserTenant = { tenantId: string; name: string; kind: string; role: string };
export type UserView = {
  userId: string; email: string; displayName: string; factorCount: number; tenants: UserTenant[];
  emailVerifiedAt?: string; suspendedAt?: string; lastSignInAt?: string; lastSuspension?: LastSuspension;
};

export type TenantStatus = 'active' | 'suspended' | 'deleting';
/** Deleting wins over suspended: a deleting tenant offers no action. */
export function tenantStatus(t: Pick<TenantRow, 'suspendedAt' | 'deletingAt'>): TenantStatus {
  if (t.deletingAt) return 'deleting';
  return t.suspendedAt ? 'suspended' : 'active';
}

/**
 * The step that finishes a half-done suspend or restore. A suspension runs AuthGate first and the Portal
 * second, so AuthGate suspended with the Portal not means the suspend stopped; the reverse means a restore did.
 */
export function missingStep(agSuspended: boolean, portalSuspended: boolean): 'suspend' | 'unsuspend' | null {
  if (agSuspended === portalSuspended) return null;
  return agSuspended ? 'suspend' : 'unsuspend';
}

/** The suspension note applies only while the subject is suspended: the backend does not track a restore. */
export function currentSuspension(s: LastSuspension | undefined, suspendedAt: string | undefined): LastSuspension | null {
  return s && suspendedAt ? s : null;
}

/** The tenant's name, or the personal label for a workspace that has none. */
export const tenantName = (t: Pick<TenantRow, 'name' | 'kind'>, personal: string): string => (t.name === '' ? personal : t.name);
