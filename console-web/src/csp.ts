/**
 * The Console's site Content-Security-Policy: no inline script, no inline style.
 *
 * An avatar is uploaded straight from the browser to S3 with a presigned POST (`fetch`, so it is
 * `connect-src`): `cspFor(host)` adds the bucket's regional endpoint, for example
 * `auth-gate-console-stg-avatars-123456789012.s3.eu-west-1.amazonaws.com`. Avatars are shown from
 * `/avatars/*` on the site's own origin, so `img-src` needs nothing (`data:` is the glass grain SVG).
 * Local development calls `cspFor()`: its upload route is same-origin. infra/console.yaml carries the
 * production header; console-web/test/csp-header.test.ts pins it to this function.
 */
export function cspFor(avatarBucketHost?: string): string {
  // A bare host name: anything else (a scheme, a path, a wildcard, a separator) would widen the policy.
  if (avatarBucketHost !== undefined && !/^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\.[a-z]{2,}$/.test(avatarBucketHost)) {
    throw new Error(`cspFor: not a bare host name: ${avatarBucketHost}`);
  }
  return [
    "default-src 'none'",
    "script-src 'self'",
    "style-src 'self'",
    "img-src 'self' data:",
    "font-src 'self'",
    `connect-src 'self'${avatarBucketHost ? ` https://${avatarBucketHost}` : ''}`,
    "manifest-src 'self'",
    "base-uri 'none'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join('; ');
}

/** The policy of local development and of `vite preview`. */
export const csp = cspFor();
