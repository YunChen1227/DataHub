import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Built assets are served by the Go server under /admin/ (DESIGN §16.0).
// In dev, /admin/api is proxied to the relay backend on :8080.
export default defineConfig({
  base: '/admin/',
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/admin/api': 'http://localhost:8080',
    },
  },
})
