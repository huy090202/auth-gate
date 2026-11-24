import { Menu } from '@mantine/core';
import { NavItem, ShellLayout, endSession } from '@authgate/web-kit/shell-layout';
import type { ReactNode } from 'react';
import { NavLink, useLocation, useNavigate } from 'react-router';
import { Brand } from './brand';
import { AccountIcon, AdminsIcon, AuditIcon, ClientsIcon, MenuIcon, PanelLeftIcon, SignOutIcon } from './icons';
import { api } from './lib/api';
import { fmt, useStrings } from './lib/i18n';
import { useMe, type Me } from './session';

/**
 * The frame of every signed-in, full-stage screen (the layout is the kit's `ShellLayout`): a sticky
 * sidebar (brand, navigation, collapse), a sticky header with the account controls, and a centred
 * content container. The sidebar can collapse to an icon rail on wide screens; below 768 px it is
 * a top bar and never collapses.
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
        {link('/audit', t.screens.audit, <AuditIcon />)}
        {me.role === 'master' && link('/admins', t.screens.admins, <AdminsIcon />)}
        {link('/account', t.screens.account, <AccountIcon />)}
      </>}
      account={{
        avatar: <span className="avatar" aria-hidden="true">{[...me.username][0]?.toUpperCase()}</span>,
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
