import { expect, test } from 'vitest';
import { errorCodes } from '../src/lib/api';
import { describe as describeError } from '../src/lib/i18n';
import { en, vi } from '../src/strings';

test('describe() gives a sentence for every ErrorCode in both languages', () => {
  expect(errorCodes.length).toBe(47);
  for (const t of [en, vi]) {
    for (const code of errorCodes) {
      const text = describeError({ code, extras: {} }, t);
      expect(text, `${t.locale} ${code}`).toMatch(/\S{2,}.*\S/);
      expect(text).toBe(t.errors[code]);
    }
    expect(describeError(new Error('x'), t)).toBe(t.errors.internal);
  }
  expect(vi.locale).not.toBe(en.locale);
});

// Typecheck proves both languages have every key; it cannot see that a translation dropped or
// renamed a {placeholder}, which would show the user a literal "{n}" or lose the value.
test('every string has the same {placeholders} in both languages', () => {
  const walk = (o: unknown, path: string, out: Map<string, string>) => {
    if (typeof o === 'string') out.set(path, [...o.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(','));
    else if (o && typeof o === 'object') for (const [k, v] of Object.entries(o)) walk(v, `${path}.${k}`, out);
  };
  const a = new Map<string, string>();
  const b = new Map<string, string>();
  walk(en, 'en', a);
  walk(vi, 'en', b);
  expect([...b]).toEqual([...a]);
});
