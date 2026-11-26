import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from 'vitest';
import { csp, cspFor } from '../src/csp';
import config from '../vite.config';

// Why: the policy lives twice, in csp.ts (the dev server and `vite preview`) and in infra/console.yaml
// (CloudFront, the production header). A drift would only show in a browser under the production policy,
// as a silently blocked avatar upload. The template's bucket name is a !Sub, so the test fills the same
// three placeholders CloudFormation does and compares with cspFor of the resulting host.
test('infra/console.yaml serves exactly cspFor(the avatar bucket regional domain), and preview serves csp', () => {
  expect(config.preview?.headers?.['Content-Security-Policy']).toBe(csp);
  const yaml = readFileSync(join(import.meta.dirname, '../../infra/console.yaml'), 'utf8');
  const fill = (t: string) =>
    t.replaceAll('${Stage}', 'stg').replaceAll('${AWS::AccountId}', '123456789012').replaceAll('${AWS::Region}', 'ap-southeast-1');
  const header = yaml.match(/^\s*ContentSecurityPolicy: !Sub "(default-src 'none'; script-src .+)"$/m);
  expect(header, 'the site policy line').not.toBeNull();
  const bucket = yaml.match(/^\s*BucketName: !Sub (auth-gate-console-\$\{Stage\}-avatars-\$\{AWS::AccountId\})$/m);
  expect(bucket, 'the avatar bucket name').not.toBeNull();
  const host = `${fill(bucket![1]!)}.s3.ap-southeast-1.amazonaws.com`;
  expect(fill(header![1]!)).toBe(cspFor(host));
});

// Why: a host that carried a scheme, path or wildcard would silently widen the production policy.
test('cspFor adds only a bare avatar bucket host to connect-src', () => {
  const host = 'auth-gate-console-stg-avatars-123456789012.s3.eu-west-1.amazonaws.com';
  expect(cspFor(host)).toContain(`connect-src 'self' https://${host};`);
  for (const bad of ['https://x.example.com', 'x.example.com/path', '*.s3.amazonaws.com', 'a b.com', 'x.com;script-src *', '']) {
    expect(() => cspFor(bad), bad).toThrow();
  }
});
