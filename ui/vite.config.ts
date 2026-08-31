import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { fileURLToPath, URL } from 'node:url';
import { mkdirSync, writeFileSync } from 'node:fs';

/**
 * Restore the //go:embed marker after every build.
 *
 * `emptyOutDir` wipes daemon/web/dist, which removes the tracked .gitkeep that
 * exists solely so `//go:embed all:dist` has something to match. A later
 * `git add -A` then commits that deletion, and a fresh clone stops compiling:
 *
 *     web/embed.go: pattern all:dist: no matching files found
 *
 * That is exactly what happened once and reached main. Recreating the marker as
 * part of the build makes the arrangement self-healing rather than a rule
 * someone has to remember.
 */
function keepEmbedMarker() {
  return {
    name: 'keep-embed-marker',
    closeBundle() {
      const dir = fileURLToPath(new URL('../daemon/web/dist', import.meta.url));
      mkdirSync(dir, { recursive: true });
      writeFileSync(`${dir}/.gitkeep`, '');
    },
  };
}

export default defineConfig({
  plugins: [vue(), keepEmbedMarker()],
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
    //   scripts/dev.sh infinite-streaming-boa.local               -> live data from a real Pi
    proxy: {
      '/api': {
        target: process.env.BOA_API || 'http://localhost:8099',
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
