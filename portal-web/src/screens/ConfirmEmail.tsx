import { Anchor, Loader, Text } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { useFragmentToken } from '../lib/useFragmentToken';
import { useMe } from '../session';
import { Card } from './parts';

/** The link sent to a new address: /confirm-email#token=... Posted once; the link works once. */
export function ConfirmEmail() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.confirmEmail));
  const { status, reload } = useMe();
  const token = useFragmentToken();
  const started = useRef(false);
  const [state, setState] = useState<'working' | 'ok' | 'expired' | 'taken' | { error: string }>(token ? 'working' : 'expired');

  useEffect(() => {
    if (!token || started.current) return;
    started.current = true;
    void (async () => {
      try {
        await api('/api/auth/confirm-email', { body: { token } });
        setState('ok');
        // A person who is signed in sees the new address at once; a failed refresh changes nothing here.
        await reload().catch(() => {});
      } catch (e) {
        if (e instanceof ApiError && e.code === 'invalid_token') setState('expired');
        else if (e instanceof ApiError && e.code === 'email_taken') setState('taken');
        else setState({ error: explain(e, t) });
      }
    })();
  }, [token, reload, t]);

  return (
    <Card title={t.screens.confirmEmail}>
      {state === 'working' && (
        <>
          <Loader size="sm" aria-label={t.loading} />
          <Text fz="sm" role="status">{t.confirmEmail.working}</Text>
        </>
      )}
      {state === 'ok' && <Text fz="sm" role="status">{t.confirmEmail.ok}</Text>}
      {state === 'expired' && <Text fz="sm" role="alert">{t.confirmEmail.expired}</Text>}
      {state === 'taken' && <Text fz="sm" role="alert">{t.confirmEmail.taken}</Text>}
      {typeof state === 'object' && <Text c="red.7" fz="sm" role="alert">{state.error}</Text>}
      {state !== 'working' && (
        status === 'full'
          ? <Anchor component={Link} to="/account" fz="sm">{t.confirmEmail.account}</Anchor>
          : <Anchor component={Link} to="/sign-in" fz="sm">{t.confirmEmail.signIn}</Anchor>
      )}
    </Card>
  );
}
