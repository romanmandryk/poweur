import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [react()],
  base: '/app/',
  build: {
    outDir: '../api/web/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '^/(messages|acks|sessions|identities|health|auth)': {
        target: process.env.VITE_RELAY_PROXY ?? 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
});
