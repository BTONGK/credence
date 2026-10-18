import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// `npm run dev` proxies /api to a running daemon (credence start).
// Open http://localhost:5173/#token=<token from ~/.credence/daemon.json>.
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true, assetsDir: 'assets', sourcemap: false, chunkSizeWarningLimit: 800 },
  server: {
    port: 5173,
    proxy: { '/api': { target: process.env.CREDENCE_URL ?? 'http://127.0.0.1:7171', changeOrigin: true } },
  },
})
