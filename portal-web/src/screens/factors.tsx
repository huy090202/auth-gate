import { Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { serializeCredential, toCreationOptions, toRequestOptions } from '@authgate/web-kit/webauthn';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { isCancelled } from '@authgate/web-kit/actions';

export type Factor = 'pin' | 'webauthn';
export type Available = { pin: boolean; webauthn: boolean };
export type Challenge = { challengeId: string; expiresAt: string; publicKey?: unknown };
export type Proof = { pin: string } | { credential: object };

/** A cancelled or timed-out browser prompt is not an error worth a red line. */
export { isCancelled };

export const passkeysSupported = () => typeof window !== 'undefined' && 'PublicKeyCredential' in window && !!navigator.credentials;

async function proofFor(factor: Factor, ch: Challenge, pin: string): Promise<Proof> {
  if (factor === 'pin') return { pin };
  const cred = (await navigator.credentials.get({ publicKey: toRequestOptions(ch.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new DOMException('No credential', 'NotAllowedError');
  return { credential: serializeCredential(cred) };
}

/** PIN or passkey against one start/verify pair. */
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
        <Group>
          <Button data-autofocus={factors.pin ? undefined : true} autoFocus={!factors.pin} variant={factors.pin ? 'default' : 'filled'}
            loading={busy === 'webauthn'} disabled={busy === 'pin'} onClick={() => void run('webauthn')}>
            {t.factor.usePasskey}
          </Button>
        </Group>
      )}
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      {notEnrolled && <Text fz="sm" role="alert">{t.factor.notSetUp}</Text>}
    </Stack>
  );
}

/**
 * One passkey registration: start, the browser prompt, finish. A /finish that is retried after a 502
 * gets 410 challenge_gone (the first attempt dropped the registration), so it starts over once.
 * A full session sends a step-up token at /start; the token is spent there, so a restart is refused
 * with step_up_required and the caller asks for a new one (useStepUp does).
 */
export async function registerPasskey(stepUpToken?: string): Promise<EnrolResult> {
  if (!passkeysSupported()) throw new DOMException('Passkeys unavailable', 'NotSupportedError');
  for (let attempt = 0; ; attempt++) {
    try {
      const reg = await api<{ registrationId: string; publicKey: unknown }>('/api/me/passkeys/start', { method: 'POST', stepUpToken });
      const cred = (await navigator.credentials.create({ publicKey: toCreationOptions(reg.publicKey) })) as PublicKeyCredential | null;
      if (!cred) throw new DOMException('No credential', 'NotAllowedError');
      return await api<EnrolResult>('/api/me/passkeys/finish', { body: { registrationId: reg.registrationId, credential: serializeCredential(cred) } });
    } catch (e) {
      if (e instanceof ApiError && e.code === 'challenge_gone' && attempt === 0) continue;
      throw e;
    }
  }
}

/** What a factor write answers. `recoveryCodes` is there only for the account's first factor. */
export type EnrolResult = { stage: 'enroll' | 'full'; recoveryCodes?: string[] };
