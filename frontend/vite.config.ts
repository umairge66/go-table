import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The Go server serves the build from internal/webui/dist. During
// development, `npm run dev` proxies /api to a server started with
// `gotable -no-window -addr 127.0.0.1:7777` (set GOTABLE_TOKEN).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: '../internal/webui/dist', emptyOutDir: true, chunkSizeWarningLimit: 2000 },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:7777',
        headers: { 'X-GoTable-Token': process.env.GOTABLE_TOKEN ?? '' },
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
