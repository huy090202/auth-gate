import { Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiError, api } from '../lib/api';
import { explain, waitSeconds } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { serializeCredential, toCreationOptions } from '../lib/webauthn';
import { isCancelled } from './SecondFactor';

/**
 * A write that can be refused with a Retry-After (409 enrollment_in_progress, 429): `report` turns
 * the error into a message and holds `waiting` true for that many seconds, so the button comes
 * back when a retry can work. The session is untouched.
 */
export function useAction() {
  const t = useStrings();
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const inFlight = useRef(false);
  const [error, setError] = useState('');
  const [note, setNote] = useState('');
  const [waiting, setWaiting] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  /**
   * Runs `fn` once at a time. Resolves true when it finished; false when it threw or `fn`
   * returned false (the user closed the step-up: nothing to report, the form keeps its input).
   */
  const run = async (fn: () => Promise<unknown>, okNote = '', key = 'action'): Promise<boolean> => {
    // First-factor writes must not double-submit: the first reissues the session, the second would get 401.
    if (inFlight.current) return false;
    inFlight.current = true;
    setBusyKey(key);
    setError('');
    setNote('');
    try {
      const done = await fn();
      if (done === false) return false;
      // A step that knows more than the caller did beforehand (a count, say) returns its own note.
      setNote(typeof done === 'string' ? done : okNote);
      return true;
    } catch (e) {
      if (isCancelled(e)) return false;
      if (e instanceof DOMException && e.name === 'InvalidStateError') setError(t.passkey.alreadyHere);
      else if (e instanceof DOMException && e.name === 'NotSupportedError') setError(t.passkey.unsupported);
      else setError(explain(e, t));
      const wait = waitSeconds(e);
      if (wait > 0) {
        setWaiting(true);
        clearTimeout(timer.current);
        timer.current = setTimeout(() => setWaiting(false), wait * 1000);
      }
      return false;
    } finally {
      inFlight.current = false;
      setBusyKey(null);
    }
  };
  /** `busy`: some action is in flight (others may be disabled). `loading(key)`: only that action's button spins. */
  return { busy: busyKey !== null, loading: (key: string) => busyKey === key, error, note, waiting, run, setError };
}

export function Feedback({ error, note }: { error: string; note: string }) {
  return (
    <>
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      {note && <Text c="green.8" fz="sm" role="status">{note}</Text>}
    </>
  );
}

/** Two matching PIN fields. `onSave` gets the PIN; the form clears when it resolves true. */
export function PinForm({ submitLabel, onSave, onCancel, disabled, loading }: {
  submitLabel: string;
  disabled?: boolean;
  loading?: boolean;
  onSave: (pin: string) => Promise<boolean>;
  onCancel?: () => void;
}) {
  const t = useStrings();
  const [pin, setPin] = useState('');
  const [again, setAgain] = useState('');
  const [mismatch, setMismatch] = useState(false);
  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    if (pin !== again) return setMismatch(true);
    setMismatch(false);
    if (await onSave(pin)) {
      setPin('');
      setAgain('');
    }
  };
  return (
    <form onSubmit={(ev) => void submit(ev)} className="panel-form">
      <Stack gap="sm">
        <PasswordInput label={t.pin.new} value={pin} onChange={(e) => setPin(e.currentTarget.value)} required
          inputMode="numeric" autoComplete="new-password" />
        <PasswordInput label={t.pin.repeat} value={again} onChange={(e) => setAgain(e.currentTarget.value)} required
          inputMode="numeric" autoComplete="new-password" error={mismatch ? t.pin.mismatch : undefined} />
        <Group justify="flex-end" gap="xs">
          {onCancel && <Button variant="subtle" color="gray" onClick={onCancel}>{t.common.cancel}</Button>}
          <Button type="submit" disabled={disabled && !loading} loading={loading}>{submitLabel}</Button>
        </Group>
      </Stack>
    </form>
  );
}

/** True when a new password was typed and equals the current one. */
export const samePassword = (current: string, next: string) => next !== '' && next === current;

/**
 * Current + new + repeat. `onSave` gets both passwords; fields clear when it resolves true.
 * `currentLabel` is "Temporary password" while enrolling.
 */
export function PasswordForm({ currentLabel, onSave, disabled, loading }: {
  currentLabel: string;
  loading?: boolean;
  onSave: (current: string, next: string) => Promise<boolean>;
  disabled?: boolean;
}) {
  const t = useStrings();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [again, setAgain] = useState('');
  const [mismatch, setMismatch] = useState(false);
  // Caught here so the user is not sent to the server for a rule it answers with a generic message.
  const same = samePassword(current, next);
  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    if (next !== again) return setMismatch(true);
    setMismatch(false);
    if (same) return;
    if (await onSave(current, next)) {
      setCurrent('');
      setNext('');
      setAgain('');
    }
  };
  return (
    <form onSubmit={(ev) => void submit(ev)} className="panel-form">
      <Stack gap="sm">
        <PasswordInput label={currentLabel} value={current} onChange={(e) => setCurrent(e.currentTarget.value)} required
          autoComplete="current-password" />
        <PasswordInput label={t.password.new} description={t.password.hint} value={next} onChange={(e) => setNext(e.currentTarget.value)}
          required autoComplete="new-password" error={same ? t.password.sameAsCurrent : undefined} />
        <PasswordInput label={t.password.repeat} value={again} onChange={(e) => setAgain(e.currentTarget.value)} required
          autoComplete="new-password" error={mismatch ? t.password.mismatch : undefined} />
        <Group justify="flex-end">
          <Button type="submit" disabled={disabled && !loading} loading={loading}>{t.password.save}</Button>
        </Group>
      </Stack>
    </form>
  );
}

/**
 * One passkey registration: start, the browser prompt, finish. `token` is the step-up token a
 * full session sends at /start (an enrolling one sends none).
 */
export async function registerPasskey(token?: string): Promise<true> {
  if (!passkeysSupported()) throw new DOMException('Passkeys unavailable', 'NotSupportedError');
  const reg = await api<{ registrationId: string; publicKey: unknown }>('/api/me/passkeys/start', { method: 'POST', stepUpToken: token });
  const cred = (await navigator.credentials.create({ publicKey: toCreationOptions(reg.publicKey) })) as PublicKeyCredential | null;
  if (!cred) throw new DOMException('No credential', 'NotAllowedError');
  await api('/api/me/passkeys/finish', { body: { registrationId: reg.registrationId, credential: serializeCredential(cred) } });
  return true; // not undefined: a closed step-up resolves undefined
}

/**
 * A /finish that is retried after a 502 gets 410 challenge_gone: the first attempt reissued the
 * session and dropped its registration. Start over from /start, once.
 */
export async function withRestart<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (e) {
    if (e instanceof ApiError && e.code === 'challenge_gone') return fn();
    throw e;
  }
}

export const passkeysSupported = () => typeof window !== 'undefined' && 'PublicKeyCredential' in window && !!navigator.credentials;
