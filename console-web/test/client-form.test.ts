import { expect, test } from 'vitest';
import { positive, whole } from '../src/screens/clientForm';
import { parsePositive } from '../src/lib/clients';
import { en } from '../src/strings';

// Why: AuthGate treats a PIN length of 0 as "keep the stored value", so an unchecked 0 would show
// "Changes saved." after a step-up while nothing changed; and a stored perSecond such as 1e-7 must
// still be accepted, or no save (a rename included) could go through.
test('the form mirrors AuthGate ranges and accepts a stored exponent form', () => {
  expect(whole('0', en, true, 'pinLength')).toBeDefined();
  expect(whole('9', en, true, 'pinLength')).toBeDefined();
  expect(whole('6', en, true, 'pinLength')).toBeUndefined();
  expect(whole('901', en, true, 'tokenTtl')).toBeDefined();
  expect(positive('5001', en)).toBeDefined();
  expect(positive('0', en)).toBeDefined();
  expect(positive(String(1e-7), en)).toBeUndefined();
  expect(parsePositive('1e-7')).toBe(1e-7);
});
