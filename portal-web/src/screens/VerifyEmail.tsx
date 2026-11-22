import { Anchor, Loader, Text } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { useFragmentToken } from '../lib/useFragmentToken';
import { useMe } from '../session';
import { Card } from './parts';

/** The link in the verification email: /verify-email#token=... Posted once; the link works once. */
export function VerifyEmail() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.verifyEmail));
  const nav = useNavigate();
  const { reload } = useMe();
  const token = useFragmentToken();
  const started = useRef(false);
  const [state, setState] = useState<'working' | 'expired' | { error: string }>(token ? 'working' : 'expired');

  useEffect(() => {
    if (!token || started.current) return;
    started.current = true;
    void (async () => {
      try {
        await api('/api/auth/verify-email', { body: { token } });
        await reload();
        nav('/enroll', { replace: true });
      } catch (e) {
        setState(e instanceof ApiError && e.code === 'invalid_token' ? 'expired' : { error: explain(e, t) });
      }
    })();
  }, [token, reload, nav, t]);

  return (
    <Card title={t.screens.verifyEmail}>
      {state === 'working' && (
        <>
          <Loader size="sm" aria-label={t.loading} />
          <Text fz="sm" role="status">{t.verifyEmail.working}</Text>
        </>
      )}
      {state === 'expired' && (
        <>
          <Text fz="sm" role="alert">{t.verifyEmail.expired}</Text>
          <Anchor component={Link} to="/sign-up/check-email" fz="sm">{t.verifyEmail.signUpAgain}</Anchor>
        </>
      )}
      {typeof state === 'object' && <Text c="red.7" fz="sm" role="alert">{state.error}</Text>}
    </Card>
  );
}
