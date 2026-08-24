import adapter from '@sveltejs/adapter-static'
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte'

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter({
      pages: '../go/internal/httpapi/spa/build',
      assets: '../go/internal/httpapi/spa/build',
      fallback: 'index.html',
      precompress: false,
      strict: true
    }),
    prerender: {
      entries: []
    },
    appDir: 'app'
  },
  compilerOptions: {
    runes: true
  }
}

export default config
