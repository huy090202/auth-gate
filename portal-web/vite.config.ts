/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// /avatars is served by the Portal in development (and by CloudFront from S3 in production).
const proxy = { '/api': 'http://localhost:8789', '/avatars': 'http://localhost:8789' };

import { csp } from './src/csp';

export default defineConfig({
  plugins: [react()],
  // font-src is 'self': no font may be inlined as a data: URI.
  build: { assetsInlineLimit: 0 },
  server: { port: 5174, proxy },
  preview: { port: 5174, proxy, headers: { 'Content-Security-Policy': csp } },
  test: { environment: 'node', css: false },
});
