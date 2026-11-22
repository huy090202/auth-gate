import { Anchor, Text } from '@mantine/core';
import { Link, useLocation } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { fmt, useStrings } from '../lib/i18n';
import { Resend } from './Resend';
import { Card } from './parts';

export function CheckEmail() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.checkEmail));
  const state = useLocation().state as { email?: unknown } | null;
  const email = typeof state?.email === 'string' ? state.email : '';
  return (
    <Card title={t.screens.checkEmail}>
      <Text fz="sm">{email ? fmt(t.checkEmail.intro, { email }) : t.checkEmail.introNoEmail}</Text>
      <Text c="dimmed" fz="sm">{t.checkEmail.noMail}</Text>
      <Resend initialEmail={email} />
      <Anchor component={Link} to="/sign-in" fz="sm">{t.common.backToSignIn}</Anchor>
    </Card>
  );
}
