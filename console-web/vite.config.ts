/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';
import { csp } from './src/csp';

// /avatars and the dev upload route are served by the Console in development (CloudFront from S3 in production).
const proxy = { '/api': 'http://localhost:8788', '/avatars': 'http://localhost:8788', '/dev/avatars-upload': 'http://localhost:8788' };

export default defineConfig({
  plugins: [react()],
  // font-src is 'self': no font may be inlined as a data: URI.
  build: { assetsInlineLimit: 0 },
  server: { proxy },
  preview: { proxy, headers: { 'Content-Security-Policy': csp } },
  test: { environment: 'node', css: false },
});
