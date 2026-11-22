import {
  ClientFields as KitClientFields, DEFAULTS, emptyForm as kitEmptyForm, formFrom as kitFormFrom, whole as kitWhole,
  type FormErrors as KitFormErrors, type FormState as KitFormState,
} from '@authgate/web-kit/client-form';
import type { ReactNode } from 'react';
import type { ClientView } from '../lib/clients';
import { MAX_PER_SECOND, RANGES, parsePositive } from '../lib/clients';
import { fmt, useStrings } from '../lib/i18n';
import type { Strings } from '../strings';

export { DEFAULTS };

/** Every field as the text in its input; parsed only when the form is submitted. */
export type FormState = KitFormState & { rateBurst: string; ratePerSecond: string };
export type FormErrors = KitFormErrors<FormState>;

export const emptyForm: FormState = { ...kitEmptyForm, rateBurst: '', ratePerSecond: '' };

export const formFrom = (c: ClientView): FormState => ({
  ...kitFormFrom(c), rateBurst: String(c.rateLimit.burst), ratePerSecond: String(c.rateLimit.perSecond),
});

/** A field that may stay empty (create) or must hold a whole number inside AuthGate's range (edit). */
export const whole = (v: string, t: Strings, required: boolean, key: keyof typeof RANGES) => kitWhole(v, t.clientForm, required, key);

export function positive(v: string, t: Strings): string | undefined {
  const n = parsePositive(v);
  if (n === null) return t.clientForm.positive;
  return n > MAX_PER_SECOND ? fmt(t.clientForm.positiveMax, { max: MAX_PER_SECOND }) : undefined;
}

export function ClientFields({ value, onChange, errors, disabled, withRate, children }: {
  value: FormState; onChange: (v: FormState) => void; errors: FormErrors; disabled?: boolean; withRate?: boolean;
  /** Placed between the name and the rest: the read-only rpId of an existing client, the rpId input of a new one. */
  children?: ReactNode;
}) {
  const t = useStrings();
  // A new client shows AuthGate's defaults as hints; an existing one shows its rate limit instead.
  return (
    <KitClientFields strings={t.clientForm} value={value} onChange={onChange} errors={errors} disabled={disabled}
      withDefaults={!withRate} withRate={withRate}>
      {children}
    </KitClientFields>
  );
}
