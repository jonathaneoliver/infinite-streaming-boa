import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    // Straight into the Go module, where //go:embed picks it up. The daemon
    // binary is the only artifact that ships.
    outDir: '../daemon/web/dist',
    emptyOutDir: true,
    // One JS file and one CSS file. The Pi serves this over a link the
    // operator may well have just throttled to 1 Mbps, so request count
    // matters more than cache granularity.
    rollupOptions: { output: { manualChunks: undefined } },
  },
  server: {
    // API calls are proxied so the browser sees one origin and there is no CORS
    // to configure. The default target is a LOCAL daemon in demo mode, because
    // the fastest loop is the one that needs no hardware:
    //
    //   scripts/dev.sh                          -> synthetic clients, no Pi
    //   scripts/dev.sh pifi.local               -> live data from a real Pi
    proxy: {
      '/api': {
        target: process.env.PIFI_API || 'http://localhost:8099',
        changeOrigin: true,
        // Server-sent events must stream. If anything in the chain buffers the
        // response, the interface sits empty until the connection closes --
        // which looks exactly like a broken backend.
        configure: (proxy) => {
          proxy.on('proxyReq', (proxyReq) => {
            proxyReq.setHeader('Accept-Encoding', 'identity');
          });
        },
      },
    },
  },
});
