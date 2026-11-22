/**
 * The Portal's site Content-Security-Policy: the Console's (no inline script, no inline style)
 * plus Cloudflare Turnstile, which needs its script and its frame. Task 12 copies this string
 * into CloudFront and asserts it is equal; the avatar bucket host is added to `img-src` then.
 */
export const csp = [
  "default-src 'none'",
  "script-src 'self' https://challenges.cloudflare.com",
  "style-src 'self'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'self'",
  "frame-src https://challenges.cloudflare.com",
  "manifest-src 'self'",
  "base-uri 'none'",
  "form-action 'self'",
  "frame-ancestors 'none'",
].join('; ');
