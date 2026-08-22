import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const libPath = fileURLToPath(new URL('./src/lib', import.meta.url));

export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: {
      $lib: libPath,
    },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:7872',
      '/v1': 'http://127.0.0.1:7872',
      '/health': 'http://127.0.0.1:7872',
      '/ws': {
        target: 'ws://127.0.0.1:7872',
        ws: true
      }
    }
  }
});
