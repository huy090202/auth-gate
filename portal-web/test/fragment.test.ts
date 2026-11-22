// @vitest-environment jsdom
import { expect, test } from 'vitest';
import { takeFragmentToken } from '../src/lib/fragment';

// Why: the token of an emailed link must not stay in the address bar or the history after it is read.
test('takeFragmentToken returns the token and removes the fragment from the URL', () => {
  history.replaceState(null, '', '/verify-email?x=1#token=abc_DEF-123');
  expect(takeFragmentToken()).toBe('abc_DEF-123');
  expect(window.location.hash).toBe('');
  expect(window.location.pathname + window.location.search).toBe('/verify-email?x=1');
  expect(takeFragmentToken()).toBe('');
});

test('a fragment without a token gives an empty string', () => {
  history.replaceState(null, '', '/reset-password#other=1');
  expect(takeFragmentToken()).toBe('');
});
