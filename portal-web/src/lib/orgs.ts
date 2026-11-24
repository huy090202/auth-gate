import type { Role } from './tenants';

export type Member = { userId: string; displayName: string; email: string; role: Role; joinedAt: string };
export type PendingInvite = {
  inviteId: string; email: string; role: Role; invitedBy: { userId: string; displayName: string }; expiresAt: string;
};

/** The roles, most powerful first. */
export const roles: Role[] = ['owner', 'admin', 'member'];

export const MAX_INVITES = 20;

export type OrgAction = 'invite' | 'change-role' | 'remove' | 'rename' | 'delete' | 'manage-client';

/**
 * What a role may do in an organisation (spec sections 3 and 8), so the screens show only controls
 * the backend would accept. `target` is the role of the member acted on. `manage-client` is
 * disabling, enabling, purging and deleting a client (parent spec section 6); renaming, origins and
 * keys are for every role. The backend stays the authority; leaving is for everyone and not in this table.
 */
export function can(role: Role, action: OrgAction, target?: Role): boolean {
  switch (action) {
    case 'invite':
      // `target` is the invite's role: only an owner invites, or re-sends to, an owner.
      return role !== 'member' && (target !== 'owner' || role === 'owner');
    case 'manage-client':
      return role !== 'member';
    case 'change-role':
      return target === undefined ? role !== 'member' : roleChoices(role, target).length > 0;
    case 'remove':
      return role === 'owner' || (role === 'admin' && target !== 'owner');
    case 'rename':
    case 'delete':
      return role === 'owner';
  }
}

/** The roles `actor` may set on a member who has `target` now; empty when none. */
export function roleChoices(actor: Role, target: Role): Role[] {
  if (actor === 'owner') return roles;
  if (actor === 'admin' && target !== 'owner') return ['admin', 'member'];
  return [];
}

const org = (tenantId: string) => `/api/t/${encodeURIComponent(tenantId)}`;
export const orgApi = {
  tenant: org,
  members: (t: string) => `${org(t)}/members`,
  member: (t: string, userId: string) => `${org(t)}/members/${encodeURIComponent(userId)}`,
  leave: (t: string) => `${org(t)}/leave`,
  invites: (t: string) => `${org(t)}/invites`,
  invite: (t: string, inviteId: string) => `${org(t)}/invites/${encodeURIComponent(inviteId)}`,
  resend: (t: string, inviteId: string) => `${org(t)}/invites/${encodeURIComponent(inviteId)}/resend`,
};
