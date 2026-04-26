import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Relative base + flat asset names so the bundle works under any URL
// prefix the Go side mounts the dashboard at (default /_/).
export default defineConfig({
  base: './',
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
  },
  server: {
    proxy: {
      '/_/api':  'http://127.0.0.1:18080',
      '/_/ws':   { target: 'ws://127.0.0.1:18080', ws: true },
      '/fn':     'http://127.0.0.1:18080',
    },
  },
})
