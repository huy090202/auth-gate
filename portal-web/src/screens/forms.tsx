import { Feedback, PasswordForm as KitPasswordForm, PinForm as KitPinForm, samePassword, useAction as useKitAction } from '@authgate/web-kit/actions';
import { explain } from '../lib/explain';
import { useStrings } from '../lib/i18n';

export { Feedback, samePassword };

/**
 * One write at a time, for the account and client screens. An error becomes a sentence under the
 * action; a Retry-After (409 enrollment_in_progress, 429) also holds `waiting` for that many seconds.
 * Success is a toast, never inline. (The behaviour is the kit's `useAction`.)
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

/** Current, new and repeat. `onSave` gets both passwords; the fields clear when it resolves true. */
export function PasswordForm(props: Omit<Parameters<typeof KitPasswordForm>[0], 'strings' | 'currentLabel'>) {
  const t = useStrings();
  return <KitPasswordForm {...props} strings={t.password} currentLabel={t.password.current} />;
}
