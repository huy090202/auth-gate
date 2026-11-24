import { Anchor, PasswordInput, Text, TextInput } from '@mantine/core';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { pendingInvite } from '../lib/invite';
import { useLang, useStrings } from '../lib/i18n';
import { useAnonGate } from '../session';
import { Card, TurnstileForm } from './parts';

/** The backend's bound: 12 to 128 bytes. */
export const passwordProblem = (pw: string): 'tooShort' | 'tooLong' | null => {
  if (pw.length < 12) return 'tooShort';
  return new TextEncoder().encode(pw).length > 128 ? 'tooLong' : null;
};

export function SignUp() {
  const t = useStrings();
  const [lang] = useLang();
  useTitle(portalTitle(t.screens.signUp));
  const nav = useNavigate();
  const [email, setEmail] = useState(() => pendingInvite()?.email ?? '');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const gate = useAnonGate();
  if (gate) return gate;

  const problem = password === '' ? null : passwordProblem(password);
  return (
    <Card title={t.screens.signUp}>
      <Text c="dimmed" fz="sm">{t.signUp.intro}</Text>
      <TurnstileForm submitLabel={t.signUp.submit} ready={passwordProblem(password) === null}
        onSubmit={async (turnstileToken) => {
          await api('/api/auth/signup', { body: { email, displayName, password, lang, turnstileToken } });
          setPassword('');
          nav('/sign-up/check-email', { state: { email }, replace: true });
        }}>
        <TextInput data-autofocus autoFocus label={t.common.email} type="email" value={email} onChange={(e) => setEmail(e.currentTarget.value)} required
          autoComplete="email" autoCapitalize="none" spellCheck={false} />
        <TextInput label={t.signUp.displayName} value={displayName} onChange={(e) => setDisplayName(e.currentTarget.value)} required
          autoComplete="name" maxLength={64} />
        <PasswordInput label={t.signUp.password} description={t.signUp.passwordHint} value={password} onChange={(e) => setPassword(e.currentTarget.value)}
          required autoComplete="new-password" error={problem ? t.signUp[problem] : undefined} />
      </TurnstileForm>
      <Text fz="sm">{t.signUp.haveAccount} <Anchor component={Link} to="/sign-in">{t.screens.signIn}</Anchor></Text>
    </Card>
  );
}
