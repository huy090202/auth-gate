import { Button, PasswordInput, Stack, Text, TextInput } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { Loading, LoadFailed, homeFor, useMe } from '../session';

export function SignIn() {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.signIn));
  const nav = useNavigate();
  const { status, error: loadError, reload, retry } = useMe();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [error, setError] = useState('');

  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={loadError} onRetry={retry} />;
  if (status !== 'anon') return <Navigate to={homeFor(status)} replace />;

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    try {
      const r = await api<{ next: 'enroll' | 'second-factor' }>('/api/auth/login', { body: { username, password } });
      setPassword('');
      await reload();
      nav(r.next === 'enroll' ? '/enroll' : '/sign-in/second-factor', { replace: true });
    } catch (e) {
      setError(explain(e, t));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  return (
    <div className="auth-stage">
      <form className="auth-card glass" onSubmit={(ev) => void submit(ev)}>
        <Stack gap="md">
          <h1>{t.screens.signIn}</h1>
          <Text c="dimmed" fz="sm">{t.signIn.intro}</Text>
          <TextInput label={t.signIn.username} value={username} onChange={(e) => setUsername(e.currentTarget.value)} required
            autoComplete="username" autoCapitalize="none" spellCheck={false} disabled={busy} />
          <PasswordInput label={t.signIn.password} value={password} onChange={(e) => setPassword(e.currentTarget.value)} required
            autoComplete="current-password" disabled={busy} />
          {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
          <Button type="submit" loading={busy}>{t.signIn.submit}</Button>
        </Stack>
      </form>
    </div>
  );
}
