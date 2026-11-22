import { Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { explain, waitSeconds } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { useToast } from '../toast';
import { isCancelled } from './factors';

/**
 * One write at a time, for the account and client screens. An error becomes a sentence under the
 * action; a Retry-After (409 enrollment_in_progress, 429) also holds `waiting` for that many seconds.
 * Success is a toast, never inline.
 */
export function useAction() {
  const t = useStrings();
  const toast = useToast();
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const inFlight = useRef(false);
  const [error, setError] = useState('');
  const [waiting, setWaiting] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  /**
   * Resolves true when `fn` finished; false when it threw or returned false (the user closed the
   * step-up: nothing to report). `fn` may return a string: a note that knows more than the caller did.
   */
  const run = async (fn: () => Promise<unknown>, okNote = '', key = 'action'): Promise<boolean> => {
    if (inFlight.current) return false;
    inFlight.current = true;
    setBusyKey(key);
    setError('');
    try {
      const done = await fn();
      if (done === false) return false;
      toast(typeof done === 'string' ? done : okNote);
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
  /** `busy`: some action is in flight. `loading(key)`: only that action's button spins. */
  return { busy: busyKey !== null, loading: (key: string) => busyKey === key, error, waiting, run, setError };
}

/** The inline error of an action. */
export function Feedback({ error }: { error: string }) {
  return error ? <Text c="red.7" fz="sm" role="alert">{error}</Text> : null;
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

/** Current, new and repeat. `onSave` gets both passwords; the fields clear when it resolves true. */
export function PasswordForm({ onSave, disabled, loading }: {
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
  const same = next !== '' && next === current;
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
        <PasswordInput label={t.password.current} value={current} onChange={(e) => setCurrent(e.currentTarget.value)} required
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
