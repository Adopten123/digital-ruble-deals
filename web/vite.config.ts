import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { apiDocsIndex } from './vite.api-docs.ts';

export default defineConfig({
  plugins: [apiDocsIndex(), react()],
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api/v1': 'http://127.0.0.1:8081',
      '/readyz': 'http://127.0.0.1:8081',
      '/healthz': 'http://127.0.0.1:8081',
    },
  },
});
