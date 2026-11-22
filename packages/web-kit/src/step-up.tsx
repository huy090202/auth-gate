import { Loader, Modal, Stack, Text } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';

/** Which second factors the user has enrolled. */
export type Available = { pin: boolean; webauthn: boolean };

/** The words of the step-up modal. */
export type StepUpStrings = { title: string; intro: string; noFactor: string; loading: string };

/* eslint-disable @typescript-eslint/no-explicit-any -- the challenge and proof shapes belong to the app's factor form */
/** What the app's factor form is given: the two calls of one step-up and what to do with its token. */
export type FactorFormSlot = (p: {
  factors: Available;
  start: (factor: any) => Promise<any>;
  verify: (challengeId: string, proof: any) => Promise<{ stepUpToken: string }>;
  onDone: (r: { stepUpToken: string }) => void;
}) => ReactNode;
/* eslint-enable @typescript-eslint/no-explicit-any */

/** What differs between the apps: words, the API client, the two paths, the factor form. */
export type StepUpConfig = {
  strings: StepUpStrings;
  explain: (e: unknown) => string;
  api: <T>(path: string, init?: { method?: string; body?: unknown }) => Promise<T>;
  /** Reads the user's enrolled factors fresh: they may have changed since the screen loaded. */
  loadFactors: () => Promise<Available>;
  startPath: string;
  verifyPath: string;
  renderFactorForm: FactorFormSlot;
};

function StepUpBody({ cfg, action, target, onToken }: { cfg: StepUpConfig; action: string; target?: string; onToken: (token: string) => void }) {
  const { strings: s, api, startPath, verifyPath, renderFactorForm } = cfg;
  const [available, setAvailable] = useState<Available | null>(null);
  const [error, setError] = useState('');
  // The app rebuilds its config every render; the body reads the factors once, when the modal opens.
  const latest = useRef(cfg);
  latest.current = cfg;
  useEffect(() => {
    let live = true;
    latest.current.loadFactors().then(
      (a) => live && setAvailable(a),
      (e) => live && setError(latest.current.explain(e)),
    );
    return () => {
      live = false;
    };
  }, []);
  return (
    <Stack>
      <Text fz="sm" c="dimmed">{s.intro}</Text>
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      {!available && !error && <Loader size="sm" aria-label={s.loading} />}
      {available && (available.pin || available.webauthn ? (
        renderFactorForm({
          factors: available,
          start: (factor) => api(startPath, { body: { action, factor, target } }),
          verify: (challengeId, proof) => api(verifyPath, { body: { challengeId, ...proof } }),
          onDone: (r) => onToken(r.stepUpToken),
        })
      ) : (
        <Text fz="sm" role="alert">{s.noFactor}</Text>
      ))}
    </Stack>
  );
}

const isStepUpRequired = (e: unknown) => typeof e === 'object' && e !== null && 'code' in e && (e as { code: unknown }).code === 'step_up_required';

/**
 * Runs `run(token)` behind a fresh step-up: the modal asks for the second factor, then the token
 * goes to `run`. Resolves undefined when the user closes the modal. A token is single use: if `run`
 * is refused with step_up_required (spent or expired) the modal opens once more; any other error
 * (including 503 busy, which wants a new step-up on the next click) reaches the caller.
 * `target` is what the token is bound to (see the spec); omit it for actions on the signed-in
 * user's own account. After a `wrong_password` the token is still valid, but the caller prompts
 * again: simpler than carrying a token across the form.
 *
 * `A` is the app's union of guarded actions.
 */
export function useStepUp<A extends string>(cfg: StepUpConfig): {
  stepUp: <T>(action: A, run: (token: string) => Promise<T>, target?: string) => Promise<T | undefined>;
  modal: ReactNode;
} {
  const [ask, setAsk] = useState<{ action: A; target?: string } | null>(null);
  const settle = useRef<((token: string | undefined) => void) | null>(null);

  const finish = useCallback((token: string | undefined) => {
    const s = settle.current;
    settle.current = null;
    setAsk(null);
    s?.(token);
  }, []);

  const getToken = useCallback(
    (action: A, target?: string) =>
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
    async <T,>(a: A, run: (token: string) => Promise<T>, target?: string): Promise<T | undefined> => {
      for (let attempt = 0; ; attempt++) {
        const token = await getToken(a, target);
        if (!token) return undefined;
        try {
          return await run(token);
        } catch (e) {
          if (isStepUpRequired(e) && attempt === 0) continue;
          throw e;
        }
      }
    },
    [getToken],
  );

  const modal = (
    <Modal opened={ask !== null} onClose={() => finish(undefined)} title={cfg.strings.title} centered>
      {ask && <StepUpBody cfg={cfg} action={ask.action} target={ask.target} onToken={finish} />}
    </Modal>
  );
  return { stepUp, modal };
}
