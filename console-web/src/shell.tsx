import { Menu } from '@mantine/core';
import { NavItem, ShellLayout, endSession } from '@authgate/web-kit/shell-layout';
import { useEffect, useState, type ReactNode } from 'react';
import { NavLink, useLocation, useNavigate } from 'react-router';
import { Brand } from './brand';
import { AccountIcon, AdminsIcon, AuditIcon, ClientsIcon, MenuIcon, PanelLeftIcon, SignOutIcon, TenantsIcon, UsersIcon } from './icons';
import { api } from './lib/api';
import { fmt, useStrings } from './lib/i18n';
import { useMe, type Me } from './session';

/** The admin's picture, or their initial when there is none or it cannot be loaded. */
export function Avatar({ me, large }: { me: Me; large?: boolean }) {
  const t = useStrings();
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [me.avatarUrl]);
  const cls = large ? 'avatar avatar-lg' : 'avatar';
  if (me.avatarUrl && !failed) {
    return <img className={cls} src={me.avatarUrl} alt={fmt(t.common.avatarOf, { name: me.username })} onError={() => setFailed(true)} />;
  }
  return <span className={cls} aria-hidden="true">{[...me.username][0]?.toUpperCase()}</span>;
}

/**
 * The frame of every signed-in, full-stage screen (the layout is the kit's `ShellLayout`): a sticky
 * sidebar (brand, navigation, collapse), a sticky header with the account controls, and a centred
 * content container. The sidebar can collapse to an icon rail on wide screens; below 768 px the sidebar gives way to a
 * header menu button and a navigation drawer.
 */
export function Shell({ me, children, collapsed }: { me: Me; children: ReactNode; collapsed?: boolean }) {
  const t = useStrings();
  const nav = useNavigate();
  const { pathname } = useLocation();
  const { clear } = useMe();
  const signOut = () => endSession(() => api('/api/auth/logout', { method: 'POST' }), () => {
    clear();
    nav('/sign-in', { replace: true });
  });
  const link = (to: string, label: string, icon: ReactNode) => <NavItem component={NavLink} to={to} label={label} icon={icon} />;
  return (
    <ShellLayout
      storageKey="console.sidebar"
      strings={t.common}
      collapsed={collapsed}
      brand={<Brand />}
      collapseIcon={<PanelLeftIcon />}
      menuIcon={<MenuIcon />}
      routeKey={pathname}
      nav={<>
        {link('/clients', t.screens.clients, <ClientsIcon />)}
        {link('/tenants', t.screens.tenants, <TenantsIcon />)}
        {link('/users', t.screens.users, <UsersIcon />)}
        {link('/audit', t.screens.audit, <AuditIcon />)}
        {me.role === 'master' && link('/admins', t.screens.admins, <AdminsIcon />)}
        {link('/account', t.screens.account, <AccountIcon />)}
      </>}
      account={{
        avatar: <Avatar me={me} />,
        name: me.username,
        buttonLabel: fmt(t.common.accountMenu, { username: me.username }),
        menu: <>
          <Menu.Label>{t.common.role[me.role]}</Menu.Label>
          <Menu.Item component={NavLink} to="/account" leftSection={<AccountIcon />}>{t.screens.account}</Menu.Item>
          <Menu.Item color="red" leftSection={<SignOutIcon />} onClick={() => void signOut()}>{t.common.signOut}</Menu.Item>
        </>,
      }}
    >
      {children}
    </ShellLayout>
  );
}
