import { Anchor, Button, PasswordInput, Stack, Text, TextInput } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { useAnonGate, useMe } from '../session';
import { Turnstile, useTurnstile } from '../turnstile';
import { Resend } from './Resend';
import { Card } from './parts';

export function SignIn() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.signIn));
  const nav = useNavigate();
  const { reload } = useMe();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [error, setError] = useState('');
  // The widget appears only once the backend asks for it (403 turnstile_required).
  const [needCheck, setNeedCheck] = useState(false);
  const [resendOpen, setResendOpen] = useState(false);
  const ts = useTurnstile();
  const gate = useAnonGate();
  if (gate) return gate;

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    try {
      const r = await api<{ next: 'enroll' | 'second-factor' }>('/api/auth/login', {
        body: { email, password, ...(needCheck ? { turnstileToken: ts.token } : {}) },
      });
      setPassword('');
      await reload();
      nav(r.next === 'enroll' ? '/enroll' : '/sign-in/second-factor', { replace: true });
    } catch (e) {
      if (e instanceof ApiError && e.code === 'turnstile_required') setNeedCheck(true);
      setError(explain(e, t));
      if (needCheck) ts.reset();
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  return (
    <Card title={t.screens.signIn}>
      <Text c="dimmed" fz="sm">{t.signIn.intro}</Text>
      <form onSubmit={(ev) => void submit(ev)}>
        <Stack gap="md">
          <TextInput data-autofocus autoFocus label={t.common.email} type="email" value={email} onChange={(e) => setEmail(e.currentTarget.value)} required
            autoComplete="username" autoCapitalize="none" spellCheck={false} disabled={busy} />
          <PasswordInput label={t.common.password} value={password} onChange={(e) => setPassword(e.currentTarget.value)} required
            autoComplete="current-password" disabled={busy} />
          {needCheck && <Turnstile {...ts.props} />}
          {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
          <Button type="submit" loading={busy} disabled={needCheck && !ts.token}>{t.signIn.submit}</Button>
        </Stack>
      </form>
      <Stack gap="xs">
        <Anchor component="button" type="button" fz="sm" ta="left" aria-expanded={resendOpen} aria-controls="resend-panel"
          onClick={() => setResendOpen((o) => !o)}>{t.signIn.noEmail}</Anchor>
        {resendOpen && <div id="resend-panel"><Resend initialEmail={email} /></div>}
      </Stack>
      <Anchor component={Link} to="/forgot" fz="sm">{t.signIn.forgot}</Anchor>
      <Text fz="sm">{t.signIn.noAccount} <Anchor component={Link} to="/sign-up">{t.signIn.createAccount}</Anchor></Text>
    </Card>
  );
}
