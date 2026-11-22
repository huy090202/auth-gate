import { Button, Text } from '@mantine/core';
import { useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { fmt, useStrings } from '../lib/i18n';
import { SessionGuard, useMe } from '../session';
import { Card } from './parts';

/** Where a full session lands. Task 11 replaces this with the account and workspace screens. */
export function Account() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.account));
  const nav = useNavigate();
  const { clear } = useMe();
  return (
    <SessionGuard>
      {({ me }) => (
        <Card title={t.screens.account}>
          <Text fz="sm">{fmt(t.account.signedIn, { email: me.email })}</Text>
          <Text c="dimmed" fz="sm">{t.account.soon}</Text>
          <Button variant="default"
            onClick={() => void api('/api/auth/logout', { method: 'POST' }).catch(() => {}).finally(() => {
              clear();
              nav('/sign-in', { replace: true });
            })}>
            {t.common.signOut}
          </Button>
        </Card>
      )}
    </SessionGuard>
  );
}
