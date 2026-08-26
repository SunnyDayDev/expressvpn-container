import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    // dev-режим: проксируем API в работающий контейнер
    proxy: {
      '/v1': 'http://127.0.0.1:48100',
      '/healthz': 'http://127.0.0.1:48100',
    },
  },
})
