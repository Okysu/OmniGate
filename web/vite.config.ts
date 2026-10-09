import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

const backend = 'http://localhost:8080'

// File watching uses inotify on Linux. When the per-user inotify instance limit
// (fs.inotify.max_user_instances, often 128) is exhausted by IDEs and other tools,
// every watch fails with EMFILE. VITE_USE_POLLING=1 switches to polling, which
// needs no inotify at all (see README: 文件监听 EMFILE).
const usePolling = process.env.VITE_USE_POLLING === '1'

// https://vite.dev/config/
export default defineConfig({
  base: '/',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    watch: {
      usePolling,
      interval: 300,
      ignored: ['**/dist/**', '**/coverage/**', '**/.git/**'],
    },
    proxy: {
      // changeOrigin: false keeps Host/Origin as localhost:5173 so the backend
      // Origin check sees the dev origin; cookies pass through untouched.
      '/api': { target: backend, changeOrigin: false },
      '/v1': { target: backend, changeOrigin: false },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
