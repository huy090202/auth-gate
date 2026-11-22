import { Anchor, Stack, Text } from '@mantine/core';
import { useState } from 'react';
import { Link, Navigate, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { Loading, LoadFailed, homeFor, passkeyCount, useMe } from '../session';
import { FactorForm, type Challenge } from './factors';
import { Card } from './parts';

export function SecondFactor() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.secondFactor));
  const nav = useNavigate();
  const { me, status, error, reload, retry, clear } = useMe();
  const [reloadError, setReloadError] = useState('');
  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={error} onRetry={retry} />;
  if (status !== 'half' || !me) return <Navigate to={homeFor(status)} replace />;

  const available = { pin: me.factors.pin, webauthn: passkeyCount(me.factors) > 0 };
  const startOver = async () => {
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } catch {
      /* a session that is already gone is what we wanted */
    }
    clear();
    nav('/sign-in');
  };
  return (
    <Card title={t.screens.secondFactor}>
      <Text c="dimmed" fz="sm">{t.secondFactor.intro}</Text>
      <FactorForm<{ stage: 'enroll' | 'full' }>
        factors={available}
        start={(factor) => api<Challenge>('/api/auth/second-factor/start', { body: { factor } })}
        verify={(challengeId, proof) => api('/api/auth/second-factor/verify', { body: { challengeId, ...proof } })}
        onDone={(r) => {
          setReloadError('');
          // The factor was accepted; if the refresh fails the user is told, not left on a silent form.
          void reload().then(
            () => nav(r.stage === 'enroll' ? '/enroll' : '/account', { replace: true }),
            (e: unknown) => setReloadError(explain(e, t)),
          );
        }}
      />
      {reloadError && <Text c="red.7" fz="sm" role="alert">{reloadError}</Text>}
      <Stack gap={6} align="flex-start">
        <Anchor component={Link} to="/sign-in/recovery" fz="sm">{t.secondFactor.useCode}</Anchor>
        <Anchor component={Link} to="/factor-reset" fz="sm">{t.secondFactor.lost}</Anchor>
        <Anchor component="button" type="button" fz="sm" ta="left" onClick={() => void startOver()}>{t.secondFactor.startOver}</Anchor>
      </Stack>
    </Card>
  );
}
