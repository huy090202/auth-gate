import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from 'vitest';
import { csp, cspFor } from '../src/csp';
import config from '../vite.config';

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

// Why: the site's CSP lives twice, in csp.ts (the dev server and `vite preview`) and in infra/portal.yaml
// (CloudFront, the production header). A drift would only show in a browser under the production policy, as
// a silently blocked Turnstile frame or avatar upload. The template's bucket name is a !Sub, so the test
// fills the same three placeholders CloudFormation does and compares with cspFor of the resulting host.
test('infra/portal.yaml serves exactly cspFor(the avatar bucket regional domain)', () => {
  const yaml = readFileSync(join(import.meta.dirname, '../../infra/portal.yaml'), 'utf8');
  const fill = (t: string) =>
    t.replaceAll('${Stage}', 'stg').replaceAll('${AWS::AccountId}', '123456789012').replaceAll('${AWS::Region}', 'ap-southeast-1');
  const header = yaml.match(/^\s*ContentSecurityPolicy: !Sub "(.+)"$/m);
  expect(header, 'the site policy line').not.toBeNull();
  const bucket = yaml.match(/^\s*BucketName: !Sub (auth-gate-portal-\$\{Stage\}-avatars-\$\{AWS::AccountId\})$/m);
  expect(bucket, 'the avatar bucket name').not.toBeNull();
  // The host is the bucket name's regional endpoint, so a renamed bucket fails here too.
  const host = `${fill(bucket![1]!)}.s3.ap-southeast-1.amazonaws.com`;
  expect(host).toBe('auth-gate-portal-stg-avatars-123456789012.s3.ap-southeast-1.amazonaws.com');
  expect(fill(header![1]!)).toBe(cspFor(host));
});
