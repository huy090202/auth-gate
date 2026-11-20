import { expect, test } from 'vitest';
import { samePassword } from '../src/screens/factors';

// The backend answers a new password equal to the current one with a generic invalid_password;
// the form must catch it first. An empty field is not "the same".
test('samePassword flags only a typed new password equal to the current one', () => {
  expect(samePassword('temporary-pass-1', 'temporary-pass-1')).toBe(true);
  expect(samePassword('temporary-pass-1', 'another-pass-123')).toBe(false);
  expect(samePassword('', '')).toBe(false);
});
