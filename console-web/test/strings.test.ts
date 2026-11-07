import { expect, test } from 'vitest';
import { errorCodes } from '../src/lib/api';
import { describe as describeError } from '../src/lib/i18n';
import { en, vi } from '../src/strings';

test('describe() gives a sentence for every ErrorCode in both languages', () => {
  expect(errorCodes.length).toBe(40);
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
