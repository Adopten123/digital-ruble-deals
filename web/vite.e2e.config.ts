import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dedicated test ports; the developer's 5173/8081 demo is left untouched.
export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1', port: 18080, strictPort: true,
    proxy: {
      '/api/v1': 'http://127.0.0.1:18081',
      '/readyz': 'http://127.0.0.1:18081',
      '/healthz': 'http://127.0.0.1:18081',
    },
  },
});
