import { expect, test } from 'vitest';
import { csp } from '../src/csp';
import config from '../vite.config';

// Task 12 copies this string into CloudFront and asserts equality with the exported constant.
test('the CSP allows Turnstile and nothing inline, and preview serves exactly it', () => {
  expect(csp).toContain("script-src 'self' https://challenges.cloudflare.com");
  expect(csp).toContain('frame-src https://challenges.cloudflare.com');
  expect(csp).toContain("connect-src 'self'");
  expect(csp).toContain("style-src 'self';");
  expect(csp).not.toContain('unsafe-inline');
  expect(csp).not.toContain('unsafe-eval');
  expect(config.preview?.headers?.['Content-Security-Policy']).toBe(csp);
});
