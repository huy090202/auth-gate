import { Anchor, Button, PasswordInput, Stack, Text } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { useStrings } from '../lib/i18n';
import { useFragmentToken } from '../lib/useFragmentToken';
import { Card, useSubmit } from './parts';
import { passwordProblem } from './SignUp';

/** The link in the reset email: /reset-password#token=... The password is checked before the link is spent. */
export function ResetPassword() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.resetPassword));
  const token = useFragmentToken();
  const [password, setPassword] = useState('');
  const [again, setAgain] = useState('');
  const [state, setState] = useState<'form' | 'done' | 'expired'>(token ? 'form' : 'expired');
  const { busy, error, setError, run } = useSubmit();

  if (state === 'done') {
    return (
      <Card title={t.screens.resetPassword}>
        <Text fz="sm" role="status">{t.resetPassword.done}</Text>
        <Anchor component={Link} to="/sign-in" fz="sm">{t.common.backToSignIn}</Anchor>
      </Card>
    );
  }
  if (state === 'expired') {
    return (
      <Card title={t.screens.resetPassword}>
        <Text fz="sm" role="alert">{t.resetPassword.expired}</Text>
        <Anchor component={Link} to="/forgot" fz="sm">{t.resetPassword.again}</Anchor>
      </Card>
    );
  }

  const problem = password === '' ? null : passwordProblem(password);
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    if (password !== again) return setError(t.resetPassword.mismatch);
    void run(async () => {
      await api('/api/auth/reset-password', { body: { token, newPassword: password } });
      setPassword('');
      setAgain('');
      setState('done');
    }, (e) => {
      // A spent or unknown link cannot be retried; a weak password can (the link is checked first).
      if (e instanceof ApiError && e.code === 'invalid_token') setState('expired');
    });
  };
  return (
    <Card title={t.screens.resetPassword}>
      <Text c="dimmed" fz="sm">{t.resetPassword.intro}</Text>
      <form onSubmit={submit}>
        <Stack gap="md">
          <PasswordInput data-autofocus autoFocus label={t.resetPassword.new} description={t.resetPassword.hint} value={password}
            onChange={(e) => setPassword(e.currentTarget.value)} required autoComplete="new-password" error={problem ? t.signUp[problem] : undefined} />
          <PasswordInput label={t.resetPassword.repeat} value={again} onChange={(e) => setAgain(e.currentTarget.value)} required autoComplete="new-password" />
          {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
          <Button type="submit" loading={busy} disabled={passwordProblem(password) !== null}>{t.resetPassword.submit}</Button>
        </Stack>
      </form>
    </Card>
  );
}
