import { Button, Group, PasswordInput, Stack, Text } from '@mantine/core';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useToast } from './toast';

/** The user closed the browser's passkey prompt. */
export const isCancelled = (e: unknown) => e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError');

/** Seconds the server asked us to wait (extras.retryAfter), at most two minutes, or 0. */
export function waitSeconds(e: unknown): number {
  const v = typeof e === 'object' && e !== null && 'extras' in e ? (e as { extras: Record<string, unknown> }).extras.retryAfter : undefined;
  return typeof v === 'number' && v > 0 ? Math.min(Math.ceil(v), 120) : 0;
}

export type ActionConfig = {
  /** A sentence for a failure. */
  explain: (e: unknown) => string;
  strings: { alreadyHere: string; unsupported: string };
};

/**
 * One write at a time, for the account and client screens. An error becomes a sentence under the
 * action; a Retry-After (409 enrollment_in_progress, 429) also holds `waiting` for that many seconds
 * so the button comes back when a retry can work. The session is untouched. Success is a toast,
 * never inline.
 */
export function useAction({ explain, strings }: ActionConfig) {
  const toast = useToast();
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const inFlight = useRef(false);
  const [error, setError] = useState('');
  const [waiting, setWaiting] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  /**
   * Runs `fn` once at a time (first-factor writes must not double-submit: the first reissues the
   * session, the second would get 401). Resolves true when it finished; false when it threw or
   * returned false (the user closed the step-up: nothing to report, the form keeps its input).
   * `fn` may return a string: a note that knows more than the caller did, shown as the toast.
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
      if (e instanceof DOMException && e.name === 'InvalidStateError') setError(strings.alreadyHere);
      else if (e instanceof DOMException && e.name === 'NotSupportedError') setError(strings.unsupported);
      else setError(explain(e));
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
  return { busy: busyKey !== null, loading: (key: string) => busyKey === key, error, waiting, run, setError };
}

/** The inline error of an action. Success goes to a toast, never here. */
export function Feedback({ error }: { error: string }) {
  return error ? <Text c="red.7" fz="sm" role="alert">{error}</Text> : null;
}

export type PinStrings = { new: string; repeat: string; mismatch: string; cancel: string };

/** Two matching PIN fields. `onSave` gets the PIN; the form clears when it resolves true. */
export function PinForm({ strings, submitLabel, onSave, onCancel, disabled, loading }: {
  strings: PinStrings;
  submitLabel: string;
  disabled?: boolean;
  loading?: boolean;
  onSave: (pin: string) => Promise<boolean>;
  onCancel?: () => void;
}) {
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
        <PasswordInput label={strings.new} value={pin} onChange={(e) => setPin(e.currentTarget.value)} required
          inputMode="numeric" autoComplete="new-password" />
        <PasswordInput label={strings.repeat} value={again} onChange={(e) => setAgain(e.currentTarget.value)} required
          inputMode="numeric" autoComplete="new-password" error={mismatch ? strings.mismatch : undefined} />
        <Group justify="flex-end" gap="xs">
          {onCancel && <Button variant="subtle" color="gray" onClick={onCancel}>{strings.cancel}</Button>}
          <Button type="submit" disabled={disabled && !loading} loading={loading}>{submitLabel}</Button>
        </Group>
      </Stack>
    </form>
  );
}

/** True when a new password was typed and equals the current one. */
export const samePassword = (current: string, next: string) => next !== '' && next === current;

export type PasswordStrings = { new: string; hint: string; repeat: string; mismatch: string; sameAsCurrent: string; save: string };

/**
 * Current + new + repeat. `onSave` gets both passwords; fields clear when it resolves true.
 * `currentLabel` is "Temporary password" while enrolling.
 */
export function PasswordForm({ strings, currentLabel, onSave, disabled, loading }: {
  strings: PasswordStrings;
  currentLabel: string;
  loading?: boolean;
  onSave: (current: string, next: string) => Promise<boolean>;
  disabled?: boolean;
}) {
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
        <PasswordInput label={strings.new} description={strings.hint} value={next} onChange={(e) => setNext(e.currentTarget.value)}
          required autoComplete="new-password" error={same ? strings.sameAsCurrent : undefined} />
        <PasswordInput label={strings.repeat} value={again} onChange={(e) => setAgain(e.currentTarget.value)} required
          autoComplete="new-password" error={mismatch ? strings.mismatch : undefined} />
        <Group justify="flex-end">
          <Button type="submit" disabled={disabled && !loading} loading={loading}>{strings.save}</Button>
        </Group>
      </Stack>
    </form>
  );
}
