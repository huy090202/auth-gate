import {
  ClientFields as KitClientFields, DEFAULTS, emptyForm, formFrom, whole as kitWhole,
  type FormErrors as KitFormErrors, type FormState,
} from '@authgate/web-kit/client-form';
import type { ReactNode } from 'react';
import type { RANGES } from '../lib/clients';
import { useStrings } from '../lib/i18n';
import type { Strings } from '../strings';

export { DEFAULTS, emptyForm, formFrom, type FormState };
export type FormErrors = KitFormErrors;

/** A field that may stay empty (create) or must hold a whole number inside AuthGate's range (edit). */
export const whole = (v: string, t: Strings, required: boolean, key: keyof typeof RANGES) => kitWhole(v, t.clientForm, required, key);

export function ClientFields({ value, onChange, errors, disabled, withDefaults, children }: {
  value: FormState; onChange: (v: FormState) => void; errors: FormErrors; disabled?: boolean;
  /** Show AuthGate's defaults as hints (a new client). */
  withDefaults?: boolean;
  /** Placed between the name and the rest: the read-only rpId of an existing client, the rpId input of a new one. */
  children?: ReactNode;
}) {
  const t = useStrings();
  return (
    <KitClientFields strings={t.clientForm} value={value} onChange={onChange} errors={errors} disabled={disabled} withDefaults={withDefaults}>
      {children}
    </KitClientFields>
  );
}
