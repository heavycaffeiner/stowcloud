import { createRequire } from 'node:module'
import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

const require = createRequire(import.meta.url)
const reactRouterDevelopment = require.resolve('react-router')
const reactRouterProduction = reactRouterDevelopment.replace(/[\\/]dist[\\/]development[\\/]/, (match) => match.replace('development', 'production'))
const reactRouterDomProduction = reactRouterProduction.replace(/[\\/]index\.mjs$/, '/dom-export.mjs')

declare const process: { env: Record<string, string | undefined> }

export default defineConfig({
  base: '/',
  plugins: [react()],
  resolve: {
    alias: [
      { find: 'react-router/dom', replacement: reactRouterDomProduction },
      { find: 'react-router', replacement: reactRouterProduction }
    ],
    conditions: ['module', 'browser', 'production', 'import', 'default']
  },
  build: {
    outDir: '../go/engine/http/spa/build',
    emptyOutDir: true,
    manifest: true,
    chunkSizeWarningLimit: 1024,
    rollupOptions: {
      input: {
        app: fileURLToPath(new URL('./index.html', import.meta.url)),
        'service-worker': fileURLToPath(new URL('./src/service-worker.ts', import.meta.url))
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
    include: ['src/**/*.{test,spec}.{js,ts,tsx}', 'tools/**/*.{test,spec}.{js,ts,tsx}'],
    globals: false
  },
  server: {
    port: 5173,
    strictPort: false,
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
          target: process.env.SC_DEV_API ?? 'https://127.0.0.1:8081',
          secure: false,
          changeOrigin: false,
          ws: pattern.startsWith('^/api')
        }
      ])
    )
  }
})
