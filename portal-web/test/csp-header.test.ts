import { expect, test } from 'vitest';
import { csp, cspFor } from '../src/csp';
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

// Why: the avatar upload is a fetch to S3, so connect-src must name the bucket's endpoint; a host that
// carried a scheme, path or wildcard would silently widen the policy that Task 12 copies into CloudFront.
test('cspFor adds only a bare avatar bucket host to connect-src', () => {
  const host = 'auth-gate-portal-stg-avatars-123456789012.s3.eu-west-1.amazonaws.com';
  expect(cspFor(host)).toContain(`connect-src 'self' https://${host};`);
  expect(cspFor()).toBe(csp);
  expect(cspFor(host).replace(` https://${host}`, '')).toBe(csp);
  for (const bad of ['https://x.example.com', 'x.example.com/path', '*.s3.amazonaws.com', 'a b.com', 'x.com;script-src *', '']) {
    expect(() => cspFor(bad), bad).toThrow();
  }
});
