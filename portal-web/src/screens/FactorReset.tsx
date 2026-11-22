import { Anchor, Button, Text } from '@mantine/core';
import { useState } from 'react';
import { Link } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { useFragmentToken } from '../lib/useFragmentToken';
import { RequestLink } from './Forgot';
import { Card, useSubmit } from './parts';

/**
 * Without a token in the fragment this asks for the link by email. With one (/factor-reset#token=...)
 * it asks the person to start the 24-hour wait: a click, not a page load, starts it.
 */
export function FactorReset() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.factorReset));
  const token = useFragmentToken();
  const [state, setState] = useState<{ dueAt: string } | 'expired' | null>(null);
  const { busy, error, run } = useSubmit();

  if (!token) {
    return (
      <Card title={t.screens.factorReset}>
        <RequestLink path="/api/auth/factor-reset/request" intro={t.factorReset.requestIntro} submit={t.factorReset.requestSubmit} sent={t.factorReset.sent} />
        <Anchor component={Link} to="/sign-in" fz="sm">{t.common.backToSignIn}</Anchor>
      </Card>
    );
  }
  if (state === 'expired') {
    return (
      <Card title={t.screens.factorReset}>
        <Text fz="sm" role="alert">{t.factorReset.expired}</Text>
        <Anchor component={Link} to="/factor-reset" fz="sm">{t.factorReset.again}</Anchor>
      </Card>
    );
  }
  if (state) {
    return (
      <Card title={t.screens.factorReset}>
        <Text fz="sm" role="status">{fmt(t.factorReset.started, { time: formatDate(state.dueAt, t) })}</Text>
        <Anchor component={Link} to="/sign-in" fz="sm">{t.common.backToSignIn}</Anchor>
      </Card>
    );
  }
  return (
    <Card title={t.screens.factorReset}>
      <Text fz="sm">{t.factorReset.confirmIntro}</Text>
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      <Button loading={busy}
        onClick={() => void run(async () => {
          const r = await api<{ dueAt: string }>('/api/auth/factor-reset/confirm', { body: { token } });
          setState({ dueAt: r.dueAt });
        }, (e) => {
          if (e instanceof ApiError && e.code === 'invalid_token') setState('expired');
        })}>
        {t.factorReset.confirmSubmit}
      </Button>
    </Card>
  );
}
