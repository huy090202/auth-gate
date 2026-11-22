import { Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useRef, useState, type FormEvent } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { Loading, LoadFailed, hasFactor, homeFor, useMe } from '../session';
import { RecoveryCodes } from './RecoveryCodes';
import { isCancelled, registerPasskey, type EnrolResult } from './factors';
import { Card } from './parts';

/**
 * A new account, or one that signed in with a recovery code, adds a PIN or a passkey. The first factor
 * of an account answers with its recovery codes; the session is already full by then, so this screen
 * holds the codes and moves on only when the person has saved them.
 */
export function Enroll() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.enroll));
  const nav = useNavigate();
  const { me, status, error: loadError, reload, retry } = useMe();
  const [pin, setPin] = useState('');
  const [again, setAgain] = useState('');
  const [busy, setBusy] = useState<'pin' | 'passkey' | null>(null);
  const inFlight = useRef(false);
  const [error, setError] = useState('');
  const [codes, setCodes] = useState<string[] | null>(null);

  const toAccount = async () => {
    try {
      await reload();
    } catch (e) {
      setError(explain(e, t));
      return;
    }
    nav('/', { replace: true });
  };

  if (codes) return <RecoveryCodes codes={codes} onContinue={() => void toAccount()} />;
  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={loadError} onRetry={retry} />;
  if (status !== 'enroll' || !me) return <Navigate to={homeFor(status)} replace />;

  const run = async (which: 'pin' | 'passkey', fn: () => Promise<EnrolResult>) => {
    // The first write reissues the session: a second one in flight would get 401.
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(which);
    setError('');
    try {
      const r = await fn();
      setPin('');
      setAgain('');
      if (r.recoveryCodes?.length) setCodes(r.recoveryCodes);
      else await toAccount();
    } catch (e) {
      if (isCancelled(e)) return;
      if (e instanceof DOMException && e.name === 'InvalidStateError') setError(t.enroll.passkeyAlreadyHere);
      else if (e instanceof DOMException && e.name === 'NotSupportedError') setError(t.enroll.passkeyUnsupported);
      else setError(explain(e, t));
    } finally {
      inFlight.current = false;
      setBusy(null);
    }
  };

  const submitPin = (ev: FormEvent) => {
    ev.preventDefault();
    if (pin !== again) return setError(t.enroll.pinMismatch);
    void run('pin', () => api<EnrolResult>('/api/me/pin', { body: { pin } }));
  };

  return (
    <Card title={t.screens.enroll}>
      <Text c="dimmed" fz="sm">{hasFactor(me.factors) ? t.enroll.introRecovery : t.enroll.intro}</Text>
      <form onSubmit={submitPin}>
        <Stack gap="sm">
          <h2 className="sub">{t.enroll.pinTitle}</h2>
          <Text fz="sm" c="dimmed">{t.enroll.pinHint}</Text>
          <PasswordInput data-autofocus autoFocus label={t.enroll.pin} value={pin} onChange={(e) => setPin(e.currentTarget.value)} required
            inputMode="numeric" autoComplete="new-password" disabled={busy !== null} />
          <PasswordInput label={t.enroll.pinRepeat} value={again} onChange={(e) => setAgain(e.currentTarget.value)} required
            inputMode="numeric" autoComplete="new-password" disabled={busy !== null} />
          <Group>
            <Button type="submit" loading={busy === 'pin'} disabled={busy === 'passkey'}>{t.enroll.pinSet}</Button>
          </Group>
        </Stack>
      </form>
      <Stack gap="sm">
        <h2 className="sub">{t.enroll.passkeyTitle}</h2>
        <Text fz="sm" c="dimmed">{t.enroll.passkeyHint}</Text>
        <Group>
          <Button variant="default" loading={busy === 'passkey'} disabled={busy === 'pin'} onClick={() => void run('passkey', registerPasskey)}>
            {t.enroll.passkeyAdd}
          </Button>
        </Group>
      </Stack>
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
    </Card>
  );
}
