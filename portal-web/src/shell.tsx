import { Menu, Select } from '@mantine/core';
import { NavItem, ShellLayout, endSession } from '@authgate/web-kit/shell-layout';
import { useEffect, useState, type ReactNode } from 'react';
import { NavLink, useLocation, useNavigate, useParams } from 'react-router';
import { Brand } from './brand';
import { LangSwitch, SchemeToggle } from './controls';
import { AccountIcon, AuditIcon, ClientsIcon, PanelLeftIcon, SignOutIcon } from './icons';
import { api } from './lib/api';
import { fmt, useStrings } from './lib/i18n';
import { auditPath, clientsPath, fetchTenants, tenantLabel, type Tenant } from './lib/tenants';
import { useMe, type Me } from './session';

/** The user's picture, or their initial when there is none or it cannot be loaded. */
export function Avatar({ me, large }: { me: Me; large?: boolean }) {
  const cls = large ? 'avatar avatar-lg' : 'avatar';
  const t = useStrings();
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [me.avatarUrl]);
  if (me.avatarUrl && !failed) {
    return <img className={cls} src={me.avatarUrl} alt={fmt(t.shell.avatarOf, { name: me.displayName })} onError={() => setFailed(true)} />;
  }
  return <span className={cls} aria-hidden="true">{[...(me.displayName || me.email)][0]?.toUpperCase()}</span>;
}

/**
 * Lists the user's workspaces (GET /api/tenants). A list that cannot be read falls back to the
 * personal workspace the session already names, so the frame never blocks a screen.
 */
function useTenants(me: Me): Tenant[] {
  const [tenants, setTenants] = useState<Tenant[] | null>(null);
  useEffect(() => {
    let live = true;
    fetchTenants().then(
      (l) => live && setTenants(l),
      () => live && setTenants(null),
    );
    return () => {
      live = false;
    };
  }, [me.userId]);
  return tenants && tenants.length > 0
    ? tenants
    : [{ tenantId: me.personalTenantId, kind: 'personal', name: '', role: 'owner' }];
}

/**
 * The frame of every signed-in screen (the layout is the kit's `ShellLayout`): a sticky sidebar
 * (brand, workspace switcher, navigation), a sticky header with the language, theme and account
 * menu, and a centred content container. The workspace is the one in the URL (`/t/:tenantId/…`);
 * the account pages sit under the personal one.
 */
export function Shell({ me, children }: { me: Me; children: ReactNode }) {
  const t = useStrings();
  const nav = useNavigate();
  const { pathname } = useLocation();
  const { tenantId: urlTenant } = useParams();
  const { clear } = useMe();
  const tenants = useTenants(me);
  const current = tenants.find((x) => x.tenantId === (urlTenant ?? me.personalTenantId)) ?? tenants[0]!;
  // The audit log is for owners and admins (spec section 6).
  const canAudit = current.role === 'owner' || current.role === 'admin';

  const signOut = () => endSession(() => api('/api/auth/logout', { method: 'POST' }), () => {
    clear();
    nav('/sign-in', { replace: true });
  });
  // Switching keeps the section: the audit log of one workspace opens the audit log of the next.
  const switchTo = (id: string | null) => {
    if (id) nav(pathname.endsWith('/audit') && urlTenant ? auditPath(id) : clientsPath(id));
  };
  const link = (to: string, label: string, icon: ReactNode) => <NavItem component={NavLink} to={to} label={label} icon={icon} />;
  return (
    <ShellLayout
      storageKey="portal.sidebar"
      strings={t.common}
      brand={<Brand />}
      collapseIcon={<PanelLeftIcon />}
      switcher={
        <div className="tenant-switch">
          <Select aria-label={t.shell.workspace} value={current.tenantId} onChange={switchTo} allowDeselect={false}
            data={tenants.map((x) => ({ value: x.tenantId, label: tenantLabel(x, t) }))} />
        </div>
      }
      nav={<>
        {link(clientsPath(current.tenantId), t.shell.clients, <ClientsIcon />)}
        {canAudit && link(auditPath(current.tenantId), t.shell.audit, <AuditIcon />)}
        {link('/account', t.shell.account, <AccountIcon />)}
      </>}
      headerStart={<><LangSwitch /><SchemeToggle /></>}
      account={{
        avatar: <Avatar me={me} />,
        name: me.displayName,
        buttonLabel: fmt(t.common.accountMenu, { name: me.displayName }),
        menu: <>
          <Menu.Label>{me.email}</Menu.Label>
          <Menu.Item component={NavLink} to="/account" leftSection={<AccountIcon />}>{t.shell.account}</Menu.Item>
          <Menu.Item color="red" leftSection={<SignOutIcon />} onClick={() => void signOut()}>{t.common.signOut}</Menu.Item>
        </>,
      }}
    >
      {children}
    </ShellLayout>
  );
}
