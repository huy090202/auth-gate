import { Loader, Modal, Stack, Text } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { ApiError, api } from './lib/api';
import { explain } from './lib/explain';
import { useStrings } from './lib/i18n';
import { FactorForm, type Available } from './screens/SecondFactor';
import { passkeyCount, type Me } from './session';

export type StepUpAction =
  | 'rotate-key' | 'revoke-previous-keys' | 'disable-client' | 'purge-subject' | 'update-client'
  | 'change-password' | 'add-factor' | 'remove-factor'
  | 'admin-create' | 'admin-reset-password' | 'admin-clear-factors' | 'admin-disable' | 'admin-enable' | 'admin-role';

function StepUpBody({ action, target, onToken }: { action: StepUpAction; target?: string; onToken: (token: string) => void }) {
  const t = useStrings();
  const [available, setAvailable] = useState<Available | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let live = true;
    // Read fresh: the factors may have changed since the screen loaded.
    api<Me>('/api/me').then(
      (m) => live && setAvailable({ pin: m.factors.pin, webauthn: passkeyCount(m.factors) > 0 }),
      (e) => live && setError(explain(e, t)),
    );
    return () => {
      live = false;
    };
  }, [t]);
  return (
    <Stack>
      <Text fz="sm" c="dimmed">{t.stepUp.intro}</Text>
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      {!available && !error && <Loader size="sm" aria-label={t.loading} />}
      {available && (available.pin || available.webauthn ? (
        <FactorForm<{ stepUpToken: string }>
          factors={available}
          start={(factor) => api('/api/stepup/start', { body: { action, factor, target } })}
          verify={(challengeId, proof) => api('/api/stepup/verify', { body: { challengeId, ...proof } })}
          onDone={(r) => onToken(r.stepUpToken)}
        />
      ) : (
        <Text fz="sm" role="alert">{t.stepUp.noFactor}</Text>
      ))}
    </Stack>
  );
}

/**
 * Runs `run(token)` behind a fresh step-up: the modal asks for the second factor, then the token
 * goes to `run`. Resolves undefined when the user closes the modal. A token is single-use: if `run`
 * is refused with step_up_required (spent or expired) the modal opens once more; any other error
 * (including 503 busy, which wants a new step-up on the next click) reaches the caller.
 * `target` is what the token is bound to (a client id, `<clientId>/<subjectId>`, a username or
 * `<username>/<role>`; see the spec); omit it for the self-service actions. After a `wrong_password` the token is still valid, but the caller prompts again: simpler than
 * carrying a token across the form.
 */
export function useStepUp(): {
  stepUp: <T>(action: StepUpAction, run: (token: string) => Promise<T>, target?: string) => Promise<T | undefined>;
  modal: ReactNode;
} {
  const t = useStrings();
  const [ask, setAsk] = useState<{ action: StepUpAction; target?: string } | null>(null);
  const settle = useRef<((token: string | undefined) => void) | null>(null);

  const finish = useCallback((token: string | undefined) => {
    const s = settle.current;
    settle.current = null;
    setAsk(null);
    s?.(token);
  }, []);

  const getToken = useCallback(
    (action: StepUpAction, target?: string) =>
      new Promise<string | undefined>((resolve) => {
        settle.current?.(undefined);
        settle.current = resolve;
        setAsk({ action, target });
      }),
    [],
  );

  // A screen that unmounts with the modal open must not leave its caller waiting.
  useEffect(() => () => settle.current?.(undefined), []);

  const stepUp = useCallback(
    async <T,>(a: StepUpAction, run: (token: string) => Promise<T>, target?: string): Promise<T | undefined> => {
      for (let attempt = 0; ; attempt++) {
        const token = await getToken(a, target);
        if (!token) return undefined;
        try {
          return await run(token);
        } catch (e) {
          if (e instanceof ApiError && e.code === 'step_up_required' && attempt === 0) continue;
          throw e;
        }
      }
    },
    [getToken],
  );

  const modal = (
    <Modal opened={ask !== null} onClose={() => finish(undefined)} title={t.stepUp.title} centered>
      {ask && <StepUpBody action={ask.action} target={ask.target} onToken={finish} />}
    </Modal>
  );
  return { stepUp, modal };
}
