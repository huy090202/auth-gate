import { Button, Stack, Text } from '@mantine/core';
import { useRef, useState, type FormEvent, type ReactNode } from 'react';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';
import { Turnstile, useTurnstile } from '../turnstile';

/** The centred glass card every screen of this part sits in. */
export function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="auth-stage">
      <Stack className="auth-card glass" gap="md">
        <h1>{title}</h1>
        {children}
      </Stack>
    </div>
  );
}

/** One request at a time, with the busy flag and the sentence for a failure. */
export function useSubmit() {
  const t = useStrings();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const inFlight = useRef(false);
  const run = async (fn: () => Promise<void>, onError?: (e: unknown) => void) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    try {
      await fn();
    } catch (e) {
      setError(explain(e, t));
      onError?.(e);
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };
  return { busy, error, setError, run };
}

/**
 * A form with a Turnstile widget. The button waits for a token; after every answer from the
 * backend the widget is reset, since a token works once.
 */
export function TurnstileForm({ submitLabel, children, onSubmit, ready = true }: {
  submitLabel: string;
  children: ReactNode;
  /** Gets the token; throw to show the failure. */
  onSubmit: (token: string) => Promise<void>;
  ready?: boolean;
}) {
  const ts = useTurnstile();
  const { busy, error, run } = useSubmit();
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    void run(() => onSubmit(ts.token)).finally(ts.reset);
  };
  return (
    <form onSubmit={submit}>
      <Stack gap="md">
        {children}
        <Turnstile {...ts.props} />
        {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
        <Button type="submit" loading={busy} disabled={!ts.token || !ready}>{submitLabel}</Button>
      </Stack>
    </form>
  );
}
