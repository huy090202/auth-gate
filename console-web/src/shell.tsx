import { Anchor, Button, Text } from '@mantine/core';
import { useState, type ReactNode } from 'react';
import { NavLink, useNavigate } from 'react-router';
import { Brand } from './brand';
import { LangSwitch, SchemeToggle } from './controls';
import { api } from './lib/api';
import { useStrings } from './lib/i18n';
import { useMe, type Me } from './session';

/**
 * The frame of every signed-in, full-stage screen: a sticky sidebar (brand, navigation, who is
 * signed in, language, theme, sign out) and a content area. Below 768 px the sidebar is a top bar.
 */
export function Shell({ me, children }: { me: Me; children: ReactNode }) {
  const t = useStrings();
  const nav = useNavigate();
  const { clear } = useMe();
  const [busy, setBusy] = useState(false);
  const signOut = async () => {
    setBusy(true);
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } catch {
      /* a session that is already gone is what we wanted */
    }
    clear();
    nav('/sign-in', { replace: true });
  };
  const link = (to: string, label: string) => (
    <Anchor component={NavLink} to={to} className="nav-link" fz="sm" underline="never">{label}</Anchor>
  );
  return (
    <div className="shell">
      <aside className="sidebar glass">
        <div className="sidebar-brand"><Brand /></div>
        <nav className="sidebar-nav" aria-label={t.common.mainNav}>
          {link('/clients', t.screens.clients)}
          {link('/audit', t.screens.audit)}
          {me.role === 'master' && link('/admins', t.screens.admins)}
          {link('/account', t.screens.account)}
        </nav>
        <div className="sidebar-foot">
          <Text fz="sm" className="sidebar-user" title={me.username}>
            <span className="sidebar-name">{me.username}</span>
            <span className="sidebar-role">{t.common.role[me.role]}</span>
          </Text>
          <div className="sidebar-tools">
            <LangSwitch />
            <SchemeToggle />
          </div>
          <Button variant="default" size="compact-sm" onClick={() => void signOut()} loading={busy}>{t.common.signOut}</Button>
        </div>
      </aside>
      <div className="shell-content">{children}</div>
    </div>
  );
}
