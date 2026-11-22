import { Anchor, Button, PasswordInput, Stack, Text } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { useStrings } from '../lib/i18n';
import { useFragmentToken } from '../lib/useFragmentToken';
import { useMe } from '../session';
import { Card, useSubmit } from './parts';

/**
 * The link in the verification email: /verify-email#token=... The account's password is asked for
 * with it, so whoever signed up with someone else's address cannot be the one who confirms it. A
 * wrong password leaves the link usable, so a refusal keeps the form for another try.
 */
export function VerifyEmail() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.verifyEmail));
  const nav = useNavigate();
  const { reload } = useMe();
  const token = useFragmentToken();
  const [password, setPassword] = useState('');
  const { busy, error, setError, run } = useSubmit();

  if (!token) {
    return (
      <Card title={t.screens.verifyEmail}>
        <Text fz="sm" role="alert">{t.verifyEmail.expired}</Text>
        <Anchor component={Link} to="/sign-up/check-email" fz="sm">{t.verifyEmail.signUpAgain}</Anchor>
      </Card>
    );
  }

  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    void run(async () => {
      await api('/api/auth/verify-email', { body: { token, password } });
      setPassword('');
      await reload();
      nav('/enroll', { replace: true });
    }, (e) => {
      // The backend answers a bad link and a wrong password alike, so the message names both.
      if (e instanceof ApiError && e.code === 'invalid_token') setError(t.verifyEmail.refused);
    });
  };

  return (
    <Card title={t.screens.verifyEmail}>
      <Text c="dimmed" fz="sm">{t.verifyEmail.intro}</Text>
      <form onSubmit={submit}>
        <Stack gap="md">
          <PasswordInput data-autofocus autoFocus label={t.common.password} value={password}
            onChange={(e) => setPassword(e.currentTarget.value)} required autoComplete="current-password" disabled={busy} />
          {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
          <Button type="submit" loading={busy} disabled={password === ''}>{t.verifyEmail.submit}</Button>
        </Stack>
      </form>
      <Anchor component={Link} to="/sign-up/check-email" fz="sm">{t.verifyEmail.signUpAgain}</Anchor>
    </Card>
  );
}
