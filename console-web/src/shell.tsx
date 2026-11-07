import { Anchor, Button, Group, Text } from '@mantine/core';
import { useState, type ReactNode } from 'react';
import { NavLink, useNavigate } from 'react-router';
import { api } from './lib/api';
import { useStrings } from './lib/i18n';
import { useMe, type Me } from './session';

/** The frame of every signed-in screen: navigation, who is signed in, sign out. */
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
    <div className="app-stage">
      <div className="app-column">
        <Group className="app-nav" component="nav" aria-label={t.common.mainNav} wrap="wrap">
          {link('/clients', t.screens.clients)}
          {link('/audit', t.screens.audit)}
          {me.role === 'master' && link('/admins', t.screens.admins)}
          {link('/account', t.screens.account)}
          <Group gap="xs" ml="auto" wrap="nowrap">
            <Text fz="sm" c="dimmed">{me.username} · {t.common.role[me.role]}</Text>
            <Button variant="default" size="compact-sm" onClick={() => void signOut()} loading={busy}>{t.common.signOut}</Button>
          </Group>
        </Group>
        {children}
      </div>
    </div>
  );
}
