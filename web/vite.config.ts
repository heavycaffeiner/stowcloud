import { sveltekit } from '@sveltejs/kit/vite'
// `vitest/config` rather than `vite`: the `test` block below is Vitest's, and
// only this re-export of `defineConfig` knows about it.
import { defineConfig } from 'vitest/config'
import { functionsMixins } from 'vite-plugin-functions-mixins'

// This file runs under Node (never bundled for the browser), so `process.env`
// below is real and correct -- but there is no @types/node in this project
// (the app itself never touches Node globals), so plain `tsc`/svelte-check
// has no ambient declaration for it. A minimal local shim beats adding a
// devDependency just for one config-time env var read.
declare const process: { env: Record<string, string | undefined> }

export default defineConfig({
  // m3-svelte ships CSS written against the `@function`/`@mixin` proposal --
  // `--translucent(...)` and `@apply --m3-body-large` are not implemented by
  // any browser. Without this plugin every elevation shadow and every type
  // style silently drops out, so it is required, not an optimisation.
  plugins: [functionsMixins({ deps: ['m3-svelte'] }), sveltekit()],
  test: {
    environment: 'jsdom',
    include: ['src/**/*.{test,spec}.{js,ts}', 'tools/**/*.{test,spec}.{js,ts}'],
    globals: false
  },
  server: {
    port: 5173,
    strictPort: false,
    // Dev only. In production the built SPA is embedded in the Rust binary and
    // served same-origin, so no proxy exists and none should: the app calls
    // `/api` relative and the server answers it. This exists purely so `vite dev` can reach a
    // separately-running `sc-server` while keeping those paths same-origin
    // from the browser's point of view — cookies are `__Host-` prefixed and
    // would not survive a cross-origin hop.
    //
    // **8081, not 8443.** 8443 is the production instance someone is actually
    // using; 8081 is the development one (`.dev/sc.toml`, its own data dir and
    // its own shares). They were the same server until an upload test wrote a
    // file into a share someone was browsing at the time. Override with
    // SC_DEV_API if you need to point somewhere else; do not point it at 8443.
    // Anchored regexes, not bare prefixes. Vite matches a string key as a
    // *prefix*, so `/s` (share links) also swallowed `/src/...`, which is how
    // dev serves every module in the app — the whole SPA went to the backend
    // and came back 401.
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
      ].map((p) => [
        p,
        {
          // `https`, because the dev server has no plaintext listener either
          // (`Config::bind`): it is the same binary. `secure: false` because
          // the certificate it presents is one it issued to itself, so there
          // is no chain for the proxy to validate; the hop is loopback.
          target: process.env.SC_DEV_API ?? 'https://127.0.0.1:8081',
          secure: false,
          changeOrigin: false,
          ws: p.startsWith('^/api')
        }
      ])
    )
  }
})
