import { Anchor, Text, TextInput } from '@mantine/core';
import { useState } from 'react';
import { Link } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { useStrings } from '../lib/i18n';
import { Card, TurnstileForm } from './parts';

/** Forgotten password and the factor-reset request are the same form against two routes. */
export function RequestLink({ path, intro, submit, sent }: { path: string; intro: string; submit: string; sent: string }) {
  const t = useStrings();
  const [email, setEmail] = useState('');
  const [done, setDone] = useState(false);
  return done ? (
    <Text fz="sm" role="status">{sent}</Text>
  ) : (
    <>
      <Text c="dimmed" fz="sm">{intro}</Text>
      <TurnstileForm submitLabel={submit}
        onSubmit={async (turnstileToken) => {
          await api(path, { body: { email, turnstileToken } });
          setDone(true);
        }}>
        <TextInput data-autofocus autoFocus label={t.common.email} type="email" value={email} onChange={(e) => setEmail(e.currentTarget.value)} required
          autoComplete="email" autoCapitalize="none" spellCheck={false} />
      </TurnstileForm>
    </>
  );
}

export function Forgot() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.forgot));
  return (
    <Card title={t.screens.forgot}>
      <RequestLink path="/api/auth/forgot" intro={t.forgot.intro} submit={t.forgot.submit} sent={t.forgot.sent} />
      <Anchor component={Link} to="/sign-in" fz="sm">{t.common.backToSignIn}</Anchor>
    </Card>
  );
}
