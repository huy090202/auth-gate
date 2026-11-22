import { Anchor, Button, Stack, Text, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { Link, Navigate, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { useStrings } from '../lib/i18n';
import { normaliseCode } from '../lib/recovery';
import { Loading, LoadFailed, homeFor, useMe } from '../session';
import { Card, useSubmit } from './parts';

/** A half session trades one recovery code for an enrolling one; the person then sets up a new factor. */
export function Recovery() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.recovery));
  const nav = useNavigate();
  const { status, error, reload, retry } = useMe();
  const [code, setCode] = useState('');
  const { busy, error: failure, run } = useSubmit();
  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={error} onRetry={retry} />;
  if (status !== 'half') return <Navigate to={homeFor(status)} replace />;

  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    void run(async () => {
      await api('/api/auth/recovery', { body: { code: normaliseCode(code) } });
      setCode('');
      await reload();
      nav('/enroll', { replace: true });
    });
  };
  return (
    <Card title={t.screens.recovery}>
      <Text c="dimmed" fz="sm">{t.recovery.intro}</Text>
      <form onSubmit={submit}>
        <Stack gap="md">
          <TextInput data-autofocus autoFocus label={t.recovery.code} description={t.recovery.hint} value={code} onChange={(e) => setCode(e.currentTarget.value)}
            required autoComplete="off" autoCapitalize="characters" spellCheck={false} disabled={busy} className="code-input" />
          {failure && <Text c="red.7" fz="sm" role="alert">{failure}</Text>}
          <Button type="submit" loading={busy}>{t.recovery.submit}</Button>
        </Stack>
      </form>
      <Stack gap={6} align="flex-start">
        <Anchor component={Link} to="/sign-in/second-factor" fz="sm">{t.recovery.back}</Anchor>
        <Anchor component={Link} to="/factor-reset" fz="sm">{t.recovery.none}</Anchor>
      </Stack>
    </Card>
  );
}
