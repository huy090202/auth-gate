import { Anchor, Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, Navigate, useNavigate } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { serializeCredential, toRequestOptions } from '@authgate/web-kit/webauthn';
import { Loading, LoadFailed, homeFor, passkeyCount, useMe } from '../session';
import { isCancelled } from '@authgate/web-kit/actions';

export type Factor = 'pin' | 'webauthn';
export type Available = { pin: boolean; webauthn: boolean };
export type Challenge = { challengeId: string; expiresAt: string; publicKey?: unknown };
export type Proof = { pin: string } | { credential: object };

/** A cancelled or timed-out browser prompt is not an error worth a red line. */
export { isCancelled };

// Copied from auth-app (app/src/app/SecondFactor.tsx).
async function proofFor(factor: Factor, ch: Challenge, pin: string): Promise<Proof> {
  if (factor === 'pin') return { pin };
  const cred = (await navigator.credentials.get({ publicKey: toRequestOptions(ch.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new DOMException('No credential', 'NotAllowedError');
  return { credential: serializeCredential(cred) };
}

/**
 * PIN or passkey against one start/verify pair. Shared by sign-in's second factor and the
 * step-up modal; only the two endpoints and what happens on success differ.
 */
export function FactorForm<T>({ factors, start, verify, onDone }: {
  factors: Available;
  start: (factor: Factor) => Promise<Challenge>;
  verify: (challengeId: string, proof: Proof) => Promise<T>;
  onDone: (result: T) => void;
}) {
  const t = useStrings();
  const [pin, setPin] = useState('');
  const [busy, setBusy] = useState<Factor | null>(null);
  const inFlight = useRef(false);
  const pinInput = useRef<HTMLInputElement>(null);
  const [error, setError] = useState('');
  const [notEnrolled, setNotEnrolled] = useState(false);
  const [lockedUntil, setLockedUntil] = useState<string | null>(null);

  const run = async (factor: Factor) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(factor);
    setError('');
    setNotEnrolled(false);
    try {
      // An expired challenge (410 challenge_gone) is restarted once without asking.
      for (let attempt = 0; ; attempt++) {
        const ch = await start(factor);
        try {
          onDone(await verify(ch.challengeId, await proofFor(factor, ch, pin)));
          return;
        } catch (e) {
          if (e instanceof ApiError && e.code === 'challenge_gone' && attempt === 0) continue;
          throw e;
        }
      }
    } catch (e) {
      if (isCancelled(e)) return;
      if (!(e instanceof ApiError)) setError(explain(e, t));
      else if (e.code === 'verification_failed') {
        const left = e.extras.attemptsRemaining;
        setError(factor === 'pin' && typeof left === 'number' ? fmt(t.factor.wrongPin, { n: left }) : t.factor.verifyFailed);
        setPin('');
      } else if (e.code === 'locked') setLockedUntil(typeof e.extras.lockedUntil === 'string' ? e.extras.lockedUntil : '');
      else if (e.code === 'factor_not_enrolled') setNotEnrolled(true);
      else setError(explain(e, t));
    } finally {
      inFlight.current = false;
      setBusy(null);
    }
  };

  // The PIN field is disabled while a request runs, which drops its focus; a wrong PIN gets it back.
  useEffect(() => {
    if (busy === null && error) pinInput.current?.focus();
  }, [busy, error]);

  const submitPin = (ev: FormEvent) => {
    ev.preventDefault();
    void run('pin');
  };

  if (lockedUntil !== null) {
    return (
      <Text role="alert">
        {t.errors.locked} {lockedUntil && fmt(t.common.lockedUntil, { time: formatDate(lockedUntil, t) })}
      </Text>
    );
  }

  return (
    <Stack>
      {factors.pin && (
        <form onSubmit={submitPin} className="factor-pin">
          <PasswordInput ref={pinInput} data-autofocus autoFocus label={t.factor.pin} value={pin} onChange={(e) => setPin(e.currentTarget.value)} required
            inputMode="numeric" autoComplete="one-time-code" disabled={busy !== null} />
          <Button type="submit" loading={busy === 'pin'} disabled={busy === 'webauthn'}>{t.factor.verifyPin}</Button>
        </form>
      )}
      {factors.pin && factors.webauthn && <Text c="dimmed" fz="sm">{t.factor.or}</Text>}
      {factors.webauthn && (
        <Group className="factor-passkey">
          {/* A passkey-only user has no field: the button takes the focus (it only focuses, the prompt still needs a press). */}
          <Button data-autofocus={factors.pin ? undefined : true} autoFocus={!factors.pin} variant={factors.pin ? 'default' : 'filled'} loading={busy === 'webauthn'} disabled={busy === 'pin'}
            onClick={() => void run('webauthn')}>
            {t.factor.usePasskey}
          </Button>
        </Group>
      )}
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      {notEnrolled && (
        <Text fz="sm" role="alert">
          {t.factor.notSetUp} <Anchor component={Link} to="/account">{t.factor.openAccount}</Anchor>
        </Text>
      )}
    </Stack>
  );
}

export function SecondFactor() {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.secondFactor));
  const nav = useNavigate();
  const { me, status, error, reload, retry, clear } = useMe();
  const [reloadError, setReloadError] = useState('');
  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={error} onRetry={retry} />;
  if (status !== 'half' || !me) return <Navigate to={homeFor(status)} replace />;

  const available = { pin: me.factors.pin, webauthn: passkeyCount(me.factors) > 0 };
  const startOver = async () => {
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } catch {
      /* a session that is already gone is what we wanted */
    }
    clear();
    nav('/sign-in');
  };
  return (
    <div className="auth-stage">
      <Stack className="auth-card glass" gap="md">
        <h1>{t.screens.secondFactor}</h1>
        <Text c="dimmed" fz="sm">{t.secondFactor.intro}</Text>
        <FactorForm<{ stage: 'enroll' | 'full' }>
          factors={available}
          start={(factor) => api<Challenge>('/api/auth/second-factor/start', { body: { factor } })}
          verify={(challengeId, proof) => api('/api/auth/second-factor/verify', { body: { challengeId, ...proof } })}
          onDone={(r) => {
            setReloadError('');
            // The factor was accepted; if the refresh fails the user is told, not left on a silent form.
            void reload().then(
              () => nav(r.stage === 'enroll' ? '/enroll' : '/clients', { replace: true }),
              (e: unknown) => setReloadError(explain(e, t)),
            );
          }}
        />
        {reloadError && <Text c="red.7" fz="sm" role="alert">{reloadError}</Text>}
        <Anchor component="button" type="button" fz="sm" ta="left" onClick={() => void startOver()}>{t.secondFactor.startOver}</Anchor>
      </Stack>
    </div>
  );
}
