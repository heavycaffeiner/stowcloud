import { existsSync, readFileSync } from 'node:fs'
import type { IncomingMessage } from 'node:http'
import path from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'
import { vanillaExtractPlugin } from '@vanilla-extract/vite-plugin'

// The engine scripts/dev.sh starts speaks only TLS and refuses a signed-in request from an http Origin,
// so `pnpm dev` serves https with the certificate dev.sh left in its data directory.
const devTls = path.resolve(fileURLToPath(new URL('..', import.meta.url)), process.env.SC_DEV_DIR ?? '.dev', 'data/tls')
const devHttps = existsSync(path.join(devTls, 'cert.pem'))
  ? { cert: readFileSync(path.join(devTls, 'cert.pem')), key: readFileSync(path.join(devTls, 'key.pem')) }
  : undefined

export default defineConfig({
  base: '/',
  plugins: [
    vanillaExtractPlugin({ identifiers: process.env.NODE_ENV === 'production' ? 'short' : 'debug' }),
    // Subscribes every component and hook that reads a signal's value to that signal.
    react({ babel: { plugins: [['module:@preact/signals-react-transform']] } })
  ],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../backend/internal/web/build',
    emptyOutDir: true,
    manifest: true,
    chunkSizeWarningLimit: 1024,
    rollupOptions: {
      input: {
        app: fileURLToPath(new URL('./index.html', import.meta.url)),
        'service-worker': fileURLToPath(new URL('./src/workers/service-worker.ts', import.meta.url))
      },
      output: {
        entryFileNames: (chunk) => (chunk.name === 'service-worker' ? 'service-worker.js' : 'app/[name]-[hash].js'),
        chunkFileNames: 'app/[name]-[hash].js',
        assetFileNames: 'app/[name]-[hash][extname]'
      }
    }
  },
  worker: {
    rollupOptions: {
      output: {
        entryFileNames: 'app/[name]-[hash].js',
        chunkFileNames: 'app/[name]-[hash].js',
        assetFileNames: 'app/[name]-[hash][extname]'
      }
    }
  },
  test: {
    environment: 'jsdom',
    include: ['tests/**/*.{test,spec}.{js,ts,tsx}', 'tools/**/*.{test,spec}.{js,ts,tsx}'],
    globals: false
  },
  server: {
    port: 5173,
    strictPort: false,
    https: devHttps,
    // Reaching this server from another machine (a phone, another laptop) needs
    // both `--host` and the name that machine uses in its address bar: Vite
    // refuses a Host it was not told about, and a DNS name is not covered by the
    // loopback and IP-literal defaults. `SC_DEV_HOSTS` takes a comma-separated
    // list, where a leading dot matches that domain and its subdomains, so a
    // tailnet name is one entry rather than a per-machine config edit.
    allowedHosts: (process.env.SC_DEV_HOSTS ?? '')
      .split(',')
      .map((host) => host.trim())
      .filter((host) => host !== ''),
    proxy: Object.fromEntries(
      [
        '^/api(/|$)',
        '^/dav(/|$)',
        '^/c/',
        '^/s/',
        '^/status\\.php$',
        '^/ocs(/|$)',
        '^/remote\\.php(/|$)',
        '^/index\\.php(/|$)'
      ].map((pattern) => [
        pattern,
        {
          target: process.env.SC_DEV_API ?? 'https://127.0.0.1:18443',
          secure: false,
          changeOrigin: false,
          ws: pattern.startsWith('^/api'),
          // Opening a share link loads the app under development; its API calls still reach the engine.
          bypass:
            pattern === '^/s/'
              ? (req: IncomingMessage) => (req.headers.accept?.includes('text/html') ? '/index.html' : undefined)
              : undefined
        }
      ])
    )
  }
})
