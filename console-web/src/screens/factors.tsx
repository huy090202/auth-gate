import { Feedback, PasswordForm as KitPasswordForm, PinForm as KitPinForm, samePassword, useAction as useKitAction } from '@authgate/web-kit/actions';
import { serializeCredential, toCreationOptions } from '@authgate/web-kit/webauthn';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';

export { Feedback, samePassword };

/**
 * A write that can be refused with a Retry-After (409 enrollment_in_progress, 429): `report` turns
 * the error into a message and holds `waiting` true for that many seconds, so the button comes
 * back when a retry can work. The session is untouched. (The behaviour is the kit's `useAction`.)
 */
export function useAction() {
  const t = useStrings();
  return useKitAction({ explain: (e) => explain(e, t), strings: t.passkey });
}

/** Two matching PIN fields. `onSave` gets the PIN; the form clears when it resolves true. */
export function PinForm(props: Omit<Parameters<typeof KitPinForm>[0], 'strings'>) {
  const t = useStrings();
  return <KitPinForm {...props} strings={{ ...t.pin, cancel: t.common.cancel }} />;
}

/**
 * Current + new + repeat. `onSave` gets both passwords; fields clear when it resolves true.
 * `currentLabel` is "Temporary password" while enrolling.
 */
export function PasswordForm(props: Omit<Parameters<typeof KitPasswordForm>[0], 'strings'>) {
  const t = useStrings();
  return <KitPasswordForm {...props} strings={t.password} />;
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
