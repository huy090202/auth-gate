import { Anchor, Menu } from '@mantine/core';
import { useState, type ReactNode } from 'react';
import { NavLink, useNavigate } from 'react-router';
import { Brand } from './brand';
import { AccountIcon, AdminsIcon, AuditIcon, ClientsIcon, PanelLeftIcon, SignOutIcon } from './icons';
import { api } from './lib/api';
import { fmt, useStrings } from './lib/i18n';
import { useMe, type Me } from './session';

const KEY = 'console.sidebar';

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(KEY) === 'collapsed';
  } catch {
    return false;
  }
}

/**
 * The frame of every signed-in, full-stage screen: a sticky sidebar (brand, navigation, collapse),
 * a sticky header with the account controls, and a centred content container. The sidebar can
 * collapse to an icon rail on wide screens; below 768 px it is a top bar and never collapses.
 */
export function Shell({ me, children, collapsed: initial }: { me: Me; children: ReactNode; collapsed?: boolean }) {
  const t = useStrings();
  const nav = useNavigate();
  const { clear } = useMe();
  const [collapsed, setCollapsed] = useState(initial ?? readCollapsed);
  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    try {
      localStorage.setItem(KEY, next ? 'collapsed' : 'expanded');
    } catch {
      /* storage blocked: the choice lasts until reload */
    }
  };
  const signOut = async () => {
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } catch {
      /* a session that is already gone is what we wanted */
    }
    clear();
    nav('/sign-in', { replace: true });
  };
  const link = (to: string, label: string, icon: ReactNode) => (
    <Anchor component={NavLink} to={to} className="nav-link" fz="sm" underline="never" title={label}>
      {icon}
      <span className="nav-label">{label}</span>
    </Anchor>
  );
  return (
    <div className="shell">
      <aside className="sidebar glass" data-collapsed={collapsed}>
        <div className="sidebar-brand"><Brand /></div>
        <nav className="sidebar-nav" aria-label={t.common.mainNav}>
          {link('/clients', t.screens.clients, <ClientsIcon />)}
          {link('/audit', t.screens.audit, <AuditIcon />)}
          {me.role === 'master' && link('/admins', t.screens.admins, <AdminsIcon />)}
          {link('/account', t.screens.account, <AccountIcon />)}
        </nav>
        <div className="sidebar-foot">
          <button type="button" className="sidebar-collapse" onClick={toggle} aria-expanded={!collapsed}
            aria-label={collapsed ? t.common.expandSidebar : t.common.collapseSidebar}
            title={collapsed ? t.common.expandSidebar : t.common.collapseSidebar}>
            <PanelLeftIcon />
            <span className="nav-label">{t.common.collapse}</span>
          </button>
        </div>
      </aside>
      <div className="shell-main">
        <header className="content-header glass">
          <Menu position="bottom-end" width={220} withinPortal={false}>
            <Menu.Target>
              <button type="button" className="avatar-btn" aria-label={fmt(t.common.accountMenu, { username: me.username })}>
                <span className="avatar" aria-hidden="true">{[...me.username][0]?.toUpperCase()}</span>
                <span className="avatar-name">{me.username}</span>
              </button>
            </Menu.Target>
            <Menu.Dropdown>
              <Menu.Label>{t.common.role[me.role]}</Menu.Label>
              <Menu.Item component={NavLink} to="/account" leftSection={<AccountIcon />}>{t.screens.account}</Menu.Item>
              <Menu.Item color="red" leftSection={<SignOutIcon />} onClick={() => void signOut()}>{t.common.signOut}</Menu.Item>
            </Menu.Dropdown>
          </Menu>
        </header>
        <div className="shell-content">
          <div className="shell-container">{children}</div>
        </div>
      </div>
    </div>
  );
}
