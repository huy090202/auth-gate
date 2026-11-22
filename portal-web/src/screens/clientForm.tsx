import { Textarea, TextInput } from '@mantine/core';
import type { ReactNode } from 'react';
import { RANGES, parseWhole, type ClientView } from '../lib/clients';
import { fmt, useStrings } from '../lib/i18n';
import type { Strings } from '../strings';

/** Every field as the text in its input; parsed only when the form is submitted. */
export type FormState = { name: string; origins: string; pinLength: string; pinMaxAttempts: string; pinLockout: string; tokenTtl: string };
export type FormErrors = Partial<Record<keyof FormState | 'rpId', string>>;

export const emptyForm: FormState = { name: '', origins: '', pinLength: '', pinMaxAttempts: '', pinLockout: '', tokenTtl: '' };

// What AuthGate applies when a create leaves a field out.
export const DEFAULTS = { pinLength: 6, pinMaxAttempts: 5, pinLockout: 900, tokenTtl: 300 } as const;

export const formFrom = (c: ClientView): FormState => ({
  name: c.name, origins: c.origins.join('\n'),
  pinLength: String(c.pinPolicy.length), pinMaxAttempts: String(c.pinPolicy.maxAttempts), pinLockout: String(c.pinPolicy.lockoutSec),
  tokenTtl: String(c.tokenTtlSec),
});

/** A field that may stay empty (create) or must hold a whole number inside AuthGate's range (edit). */
export function whole(v: string, t: Strings, required: boolean, key: keyof typeof RANGES): string | undefined {
  if (v.trim() === '') return required ? t.clientForm.whole : undefined;
  const n = parseWhole(v);
  if (n === null) return t.clientForm.whole;
  const [min, max] = RANGES[key];
  return n < min || n > max ? fmt(t.clientForm.range, { min, max }) : undefined;
}

export function ClientFields({ value, onChange, errors, disabled, withDefaults, children }: {
  value: FormState; onChange: (v: FormState) => void; errors: FormErrors; disabled?: boolean;
  /** Show AuthGate's defaults as hints (a new client). */
  withDefaults?: boolean;
  /** Placed between the name and the rest: the read-only rpId of an existing client, the rpId input of a new one. */
  children?: ReactNode;
}) {
  const t = useStrings();
  const set = (k: keyof FormState) => (e: { currentTarget: { value: string } }) => onChange({ ...value, [k]: e.currentTarget.value });
  const num = (k: Exclude<keyof FormState, 'name' | 'origins'>, label: string, dflt: number) => (
    <TextInput label={label} description={withDefaults ? fmt(t.clientForm.defaultIs, { n: dflt }) : undefined} inputMode="numeric"
      value={value[k]} onChange={set(k)} error={errors[k]} disabled={disabled} />
  );
  return (
    <div className="form-stack">
      <TextInput label={t.clientForm.name} value={value.name} onChange={set('name')} error={errors.name} disabled={disabled} required />
      {children}
      <Textarea label={t.clientForm.origins} description={t.clientForm.originsHint} value={value.origins} onChange={set('origins')}
        error={errors.origins} disabled={disabled} rows={3} required spellCheck={false} />
      <div className="form-row">
        {num('pinLength', t.clientForm.pinLength, DEFAULTS.pinLength)}
        {num('pinMaxAttempts', t.clientForm.pinMaxAttempts, DEFAULTS.pinMaxAttempts)}
        {num('pinLockout', t.clientForm.pinLockout, DEFAULTS.pinLockout)}
      </div>
      <div className="form-row">
        {num('tokenTtl', t.clientForm.tokenTtl, DEFAULTS.tokenTtl)}
      </div>
    </div>
  );
}
