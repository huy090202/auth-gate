import { Textarea, TextInput } from '@mantine/core';
import type { ReactNode } from 'react';
import { fmt } from './lib/i18n';
import { RANGES, parseWhole } from './lib/clients';

/** Every field as the text in its input; parsed only when the form is submitted. */
export type FormState = {
  name: string; origins: string; pinLength: string; pinMaxAttempts: string; pinLockout: string; tokenTtl: string;
};
export type FormErrors<T extends FormState = FormState> = Partial<Record<keyof T | 'rpId', string>>;

export const emptyForm: FormState = { name: '', origins: '', pinLength: '', pinMaxAttempts: '', pinLockout: '', tokenTtl: '' };

// What AuthGate applies when a create leaves a field out.
export const DEFAULTS = { pinLength: 6, pinMaxAttempts: 5, pinLockout: 900, tokenTtl: 300 } as const;

type ClientLike = {
  name: string; origins: string[];
  pinPolicy: { length: number; maxAttempts: number; lockoutSec: number };
  tokenTtlSec: number;
};
export const formFrom = (c: ClientLike): FormState => ({
  name: c.name, origins: c.origins.join('\n'),
  pinLength: String(c.pinPolicy.length), pinMaxAttempts: String(c.pinPolicy.maxAttempts), pinLockout: String(c.pinPolicy.lockoutSec),
  tokenTtl: String(c.tokenTtlSec),
});

export type ClientFormStrings = {
  name: string; origins: string; originsHint: string;
  pinLength: string; pinMaxAttempts: string; pinLockout: string; tokenTtl: string;
  defaultIs: string; whole: string; range: string;
  rateBurst?: string; ratePerSecond?: string;
};

/** A field that may stay empty (create) or must hold a whole number inside AuthGate's range (edit). */
export function whole(v: string, t: Pick<ClientFormStrings, 'whole' | 'range'>, required: boolean, key: keyof typeof RANGES): string | undefined {
  if (v.trim() === '') return required ? t.whole : undefined;
  const n = parseWhole(v);
  if (n === null) return t.whole;
  const [min, max] = RANGES[key];
  return n < min || n > max ? fmt(t.range, { min, max }) : undefined;
}

type RateKey = 'rateBurst' | 'ratePerSecond';

/**
 * The fields of a client. `withDefaults` shows AuthGate's defaults as hints (a new client).
 * `withRate` adds the two rate-limit fields (the state then carries them, and `strings` names them).
 */
export function ClientFields<T extends FormState>({ strings: s, value, onChange, errors, disabled, withDefaults, withRate, children }: {
  strings: ClientFormStrings;
  value: T; onChange: (v: T) => void; errors: FormErrors<T>; disabled?: boolean;
  withDefaults?: boolean; withRate?: boolean;
  /** Placed between the name and the rest: the read-only rpId of an existing client, the rpId input of a new one. */
  children?: ReactNode;
}) {
  const set = (k: string) => (e: { currentTarget: { value: string } }) => onChange({ ...value, [k]: e.currentTarget.value });
  const text = (k: string) => (value as unknown as Record<string, string>)[k] ?? '';
  const err = (k: string) => (errors as Record<string, string | undefined>)[k];
  const num = (k: Exclude<keyof FormState, 'name' | 'origins'> | RateKey, label: string, dflt?: number) => (
    <TextInput label={label} description={withDefaults && dflt !== undefined ? fmt(s.defaultIs, { n: dflt }) : undefined}
      inputMode="numeric" value={text(k)} onChange={set(k)} error={err(k)} disabled={disabled} />
  );
  return (
    <div className="form-stack">
      <TextInput label={s.name} value={value.name} onChange={set('name')} error={err('name')} disabled={disabled} required />
      {children}
      <Textarea label={s.origins} description={s.originsHint} value={value.origins} onChange={set('origins')}
        error={err('origins')} disabled={disabled} rows={3} required spellCheck={false} />
      <div className="form-row">
        {num('pinLength', s.pinLength, DEFAULTS.pinLength)}
        {num('pinMaxAttempts', s.pinMaxAttempts, DEFAULTS.pinMaxAttempts)}
        {num('pinLockout', s.pinLockout, DEFAULTS.pinLockout)}
      </div>
      <div className="form-row">
        {num('tokenTtl', s.tokenTtl, DEFAULTS.tokenTtl)}
        {withRate && num('rateBurst', s.rateBurst ?? '')}
        {withRate && num('ratePerSecond', s.ratePerSecond ?? '')}
      </div>
    </div>
  );
}
