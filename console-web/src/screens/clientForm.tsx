import { Textarea, TextInput } from '@mantine/core';
import type { ReactNode } from 'react';
import type { ClientView } from '../lib/clients';
import { parsePositive, parseWhole } from '../lib/clients';
import { fmt, useStrings } from '../lib/i18n';
import type { Strings } from '../strings';

/** Every field as the text in its input; parsed only when the form is submitted. */
export type FormState = {
  name: string; origins: string;
  pinLength: string; pinMaxAttempts: string; pinLockout: string;
  tokenTtl: string; rateBurst: string; ratePerSecond: string;
};
export type FormErrors = Partial<Record<keyof FormState, string>>;

export const emptyForm: FormState = {
  name: '', origins: '', pinLength: '', pinMaxAttempts: '', pinLockout: '', tokenTtl: '', rateBurst: '', ratePerSecond: '',
};

// What AuthGate applies when a create leaves a field out.
export const DEFAULTS = { pinLength: 6, pinMaxAttempts: 5, pinLockout: 900, tokenTtl: 300 } as const;

export const formFrom = (c: ClientView): FormState => ({
  name: c.name, origins: c.origins.join('\n'),
  pinLength: String(c.pinPolicy.length), pinMaxAttempts: String(c.pinPolicy.maxAttempts), pinLockout: String(c.pinPolicy.lockoutSec),
  tokenTtl: String(c.tokenTtlSec), rateBurst: String(c.rateLimit.burst), ratePerSecond: String(c.rateLimit.perSecond),
});

/** A field that may stay empty (create) or must hold a whole number (edit). */
export function whole(v: string, t: Strings, required: boolean): string | undefined {
  if (v.trim() === '') return required ? t.clientForm.whole : undefined;
  return parseWhole(v) === null ? t.clientForm.whole : undefined;
}
export function positive(v: string, t: Strings): string | undefined {
  return parsePositive(v) === null ? t.clientForm.positive : undefined;
}

export function ClientFields({ value, onChange, errors, disabled, withRate, children }: {
  value: FormState; onChange: (v: FormState) => void; errors: FormErrors; disabled?: boolean; withRate?: boolean;
  /** Placed between the name and the rest: the read-only rpId of an existing client, the rpId input of a new one. */
  children?: ReactNode;
}) {
  const t = useStrings();
  const set = (k: keyof FormState) => (e: { currentTarget: { value: string } }) => onChange({ ...value, [k]: e.currentTarget.value });
  const num = (k: keyof FormState, label: string, hint?: string) => (
    <TextInput label={label} description={hint} inputMode="numeric" value={value[k]} onChange={set(k)} error={errors[k]} disabled={disabled} />
  );
  const dflt = (n: number) => fmt(t.clientForm.defaultIs, { n });
  return (
    <div className="form-stack">
      <TextInput label={t.clientForm.name} value={value.name} onChange={set('name')} error={errors.name} disabled={disabled} required />
      {children}
      <Textarea label={t.clientForm.origins} description={t.clientForm.originsHint} value={value.origins} onChange={set('origins')}
        error={errors.origins} disabled={disabled} rows={3} required spellCheck={false} />
      <div className="form-row">
        {num('pinLength', t.clientForm.pinLength, withRate ? undefined : dflt(DEFAULTS.pinLength))}
        {num('pinMaxAttempts', t.clientForm.pinMaxAttempts, withRate ? undefined : dflt(DEFAULTS.pinMaxAttempts))}
        {num('pinLockout', t.clientForm.pinLockout, withRate ? undefined : dflt(DEFAULTS.pinLockout))}
      </div>
      <div className="form-row">
        {num('tokenTtl', t.clientForm.tokenTtl, withRate ? undefined : dflt(DEFAULTS.tokenTtl))}
        {withRate && num('rateBurst', t.clientForm.rateBurst)}
        {withRate && num('ratePerSecond', t.clientForm.ratePerSecond)}
      </div>
    </div>
  );
}
