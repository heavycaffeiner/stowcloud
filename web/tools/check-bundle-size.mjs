// Enforces the two byte budgets: initial JS gzip < 150 KB, share-link page
// JS gzip < 60 KB marginal. Run after `npm run build`; reads Vite's own
// build output rather than re-implementing bundling, so this can never drift from what a
// browser actually downloads.
//
// "Scroll frame drops (100k rows): 0" (the §8 table's third row) is not
// checked here — that's a runtime Core Web Vitals measurement, not a build
// artifact, and needs a real browser trace (Lighthouse/Playwright) to
// produce at all. Deliberately not wired into CI: a GitHub-hosted runner's
// CPU/IO throughput varies run to run enough that a frame-timing gate would
// be flaky, and a gate everyone learns to ignore is worse than no gate
// (hence "flaky gate" is not shipped here).
import { gzipSync } from 'node:zlib'
import { readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const webRoot = path.dirname(path.dirname(fileURLToPath(import.meta.url)))
// Where the adapter actually writes, which is the Go package that embeds the
// bundle: `//go:embed` cannot name a path outside its own package directory.
// This read `web/build`, the old location, so on a clean checkout it found
// nothing and failed with "run npm run build first" after a build had just
// run. The path is taken from svelte.config.js rather than restated, so the
// two cannot drift again.
const buildDir = path.resolve(webRoot, outputDir())
const clientManifestPath = path.join(webRoot, '.svelte-kit/output/client/.vite/manifest.json')
const serverManifestPath = path.join(webRoot, '.svelte-kit/output/server/manifest-full.js')
const indexHtmlPath = path.join(buildDir, 'index.html')

// outputDir reads the adapter's `pages` out of svelte.config.js.
//
// Read from the source text rather than by importing it: adapter-static keeps
// its options private, so the constructed adapter does not carry `pages` back.
function outputDir() {
  const src = readFileSync(path.join(webRoot, 'svelte.config.js'), 'utf8')
  const m = /pages:\s*'([^']+)'/.exec(src)
  if (!m) {
    console.error('check-bundle-size: svelte.config.js names no adapter output directory.')
    process.exit(1)
  }
  return m[1]
}

const INITIAL_JS_BUDGET = 150 * 1024
const SHARE_PAGE_JS_BUDGET = 60 * 1024

for (const [label, p] of [
  ['build/index.html', indexHtmlPath],
  ['.svelte-kit/output/client/.vite/manifest.json', clientManifestPath],
  ['.svelte-kit/output/server/manifest-full.js', serverManifestPath]
]) {
  if (!existsSync(p)) {
    console.error(`check-bundle-size: ${label} not found — run \`npm run build\` first.`)
    process.exit(1)
  }
}

function gzipSize(relFile) {
  const bytes = readFileSync(path.join(buildDir, relFile))
  return gzipSync(bytes, { level: 9 }).length
}

// ── Initial JS: every module the SPA shell preloads before any route code
// runs (`build/index.html`'s own <link rel="modulepreload"> list) — the same
// bytes on every URL, since this is a single-fallback SPA (one build/*.html,
// no per-route prerender). ──
const html = readFileSync(indexHtmlPath, 'utf8')
const initialFiles = [...html.matchAll(/rel="modulepreload"[^>]*href="([^"]+)"|href="([^"]+)"[^>]*rel="modulepreload"/g)]
  .map((m) => (m[1] ?? m[2]).replace(/^\//, ''))
  .filter((f) => f.endsWith('.js'))

if (initialFiles.length === 0) {
  console.error('check-bundle-size: found no modulepreload <link> in build/index.html — did the SvelteKit output shape change?')
  process.exit(1)
}

const initialBytes = initialFiles.reduce((sum, f) => sum + gzipSize(f), 0)

// ── Share-link page JS: the marginal JS `/s/[token]` adds on top of the
// initial shell — its leaf node's own module graph, walked via the client
// build manifest, minus whatever the initial shell already paid for. ──
const clientManifest = JSON.parse(readFileSync(clientManifestPath, 'utf8'))
const { manifest: serverManifest } = await import(pathToFileUrl(serverManifestPath))
const shareRoute = serverManifest._.routes.find((r) => r.id === '/s/[token]')
if (!shareRoute) {
  console.error('check-bundle-size: no "/s/[token]" route in the server manifest — did the share-link route move or get renamed?')
  process.exit(1)
}

const leafId = shareRoute.page.leaf
const leafKey = `.svelte-kit/generated/client-optimized/nodes/${leafId}.js`

function closureFiles(key, seen = new Set()) {
  const entry = clientManifest[key]
  if (!entry || seen.has(key)) return seen
  seen.add(key)
  if (entry.file) seen.fileSet = (seen.fileSet ?? new Set()).add(entry.file)
  for (const dep of entry.imports ?? []) closureFiles(dep, seen)
  return seen
}

const closure = closureFiles(leafKey)
const shareFiles = [...(closure.fileSet ?? [])].filter((f) => f.endsWith('.js'))
const initialFileSet = new Set(initialFiles)
const marginalFiles = shareFiles.filter((f) => !initialFileSet.has(f))
const shareBytes = marginalFiles.reduce((sum, f) => sum + gzipSize(f), 0)

function report(name, actual, budget) {
  const ok = actual <= budget
  const line = `${ok ? 'PASS' : 'FAIL'}  ${name}: ${(actual / 1024).toFixed(1)} KB (budget ${(budget / 1024).toFixed(0)} KB)`
  console.log(line)
  return ok
}

const okInitial = report('Initial JS (gzip)', initialBytes, INITIAL_JS_BUDGET)
const okShare = report('Share-link page JS (gzip, marginal)', shareBytes, SHARE_PAGE_JS_BUDGET)

if (!okInitial || !okShare) {
  console.error('\ncheck-bundle-size: a budget was exceeded.')
  process.exit(1)
}

function pathToFileUrl(p) {
  return new URL(`file://${p.replace(/\\/g, '/')}`).href
}
