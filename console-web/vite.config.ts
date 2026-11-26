/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// /avatars and the dev upload route are served by the Console in development (CloudFront from S3 in production).
const proxy = { '/api': 'http://localhost:8788', '/avatars': 'http://localhost:8788', '/dev/avatars-upload': 'http://localhost:8788' };

// The production policy from infra/console.yaml (Task 15): no inline style, no inline script.
const csp = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'self'",
  "manifest-src 'self'",
  "base-uri 'none'",
  "form-action 'self'",
  "frame-ancestors 'none'",
].join('; ');

export default defineConfig({
  plugins: [react()],
  // font-src is 'self': no font may be inlined as a data: URI.
  build: { assetsInlineLimit: 0 },
  server: { proxy },
  preview: { proxy, headers: { 'Content-Security-Policy': csp } },
  test: { environment: 'node', css: false },
});
