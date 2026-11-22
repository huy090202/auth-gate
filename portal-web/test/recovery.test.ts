import { expect, test } from 'vitest';
import { normaliseCode } from '../src/lib/recovery';

// Why: a code typed the way people type it must reach the same hash the backend stored. These are
// the cases of TestNormaliseCode in internal/portal/auth_test.go; change both together.
test('normaliseCode matches the backend for the same inputs', () => {
  const cases: Record<string, string> = {
    'abcde-fghij': 'ABCDE-FGHIJ',
    ABCDEFGHIJ: 'ABCDE-FGHIJ',
    ' abcde fghij ': 'ABCDE-FGHIJ',
    'ab-cd-ef-gh-ij': 'ABCDE-FGHIJ',
    short: 'SHORT',
  };
  for (const [input, want] of Object.entries(cases)) expect(normaliseCode(input), input).toBe(want);
});
