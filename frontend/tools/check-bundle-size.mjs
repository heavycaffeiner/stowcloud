// Enforces the two byte budgets for the browser payload: initial JavaScript gzip
// is at most 256 KiB and the public-share route adds at most 60 KiB marginal.
// The Svelte-era initial limit was 150 KiB; the React 19, TanStack Router,
// TanStack Query and mdui boot graph measured 211.1 KiB gzip, so 256 KiB leaves
// about 20% headroom. It also fails when a page that runs without a session
// pulls in the signed-in shell.
// Run after `pnpm build`; Vite's manifest is the source of truth for emitted chunks.

import { gzipSync } from 'node:zlib'
import { readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const webRoot = path.dirname(path.dirname(fileURLToPath(import.meta.url)))
const buildDir = path.resolve(webRoot, '../backend/internal/web/build')
const manifestPath = path.join(buildDir, '.vite', 'manifest.json')
const indexHtmlPath = path.join(buildDir, 'index.html')

const INITIAL_JS_BUDGET = 256 * 1024
// Measured 211.1 KiB initial and 42.3 KiB public-share marginal on 2026-10-01.
const SHARE_PAGE_JS_BUDGET = 60 * 1024

for (const [label, file] of [
  ['build/index.html', indexHtmlPath],
  ['build/.vite/manifest.json', manifestPath]
]) {
  if (!existsSync(file)) {
    console.error(`check-bundle-size: ${label} not found: run \`pnpm build\` first.`)
    process.exit(1)
  }
}

const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
const html = readFileSync(indexHtmlPath, 'utf8')
const records = Object.entries(manifest)
const byFile = new Map(
  records.filter(([, value]) => typeof value.file === 'string').map(([key, value]) => [value.file, { key, ...value }])
)

/** @param {string} value */
function relativeAsset(value) {
  return value.replace(/^\//, '').replace(/^\.\//, '')
}

/** @param {string} file */
function gzipSize(file) {
  return gzipSync(readFileSync(path.join(buildDir, relativeAsset(file))), { level: 9 }).length
}

/**
 * @param {string} key
 * @param {Set<string>} files
 * @param {Set<string>} seen
 */
function addRecordClosure(key, files = new Set(), seen = new Set(), includeDynamic = false) {
  if (seen.has(key)) return files
  seen.add(key)
  const record = manifest[key]
  if (!record) return files
  if (record.file) files.add(relativeAsset(record.file))
  for (const dependency of record.imports ?? []) addRecordClosure(dependency, files, seen, includeDynamic)
  // Vite's HTML entry lists every lazy route in dynamicImports. That list is
  // the application's route registry, not a dependency of a public route.
  // Follow dynamic imports for the selected route and its ordinary chunks, but
  // never recurse through index.html or the public closure would absorb the
  // authenticated shell and every admin route.
  if (includeDynamic && key !== 'index.html') {
    for (const dependency of record.dynamicImports ?? []) addRecordClosure(dependency, files, seen, includeDynamic)
  }
  return files
}

/** @param {string} file */
function recordKeyForFile(file) {
  return byFile.get(relativeAsset(file))?.key
}

// Vite normally emits one module script and may emit modulepreloads. Count the
// files reachable from those records once, rather than counting duplicate
// imports or trying to infer chunks from their names.
const initialFiles = new Set()
const initialRefs = [
  ...[...html.matchAll(/<script[^>]+type=["']module["'][^>]+src=["']([^"']+)["']/gi)].map((m) => m[1]),
  ...[...html.matchAll(/<link[^>]+rel=["']modulepreload["'][^>]+href=["']([^"']+)["']/gi)].map((m) => m[1]),
  ...[...html.matchAll(/<link[^>]+href=["']([^"']+)["'][^>]+rel=["']modulepreload["']/gi)].map((m) => m[1])
]
for (const ref of initialRefs) {
  const file = relativeAsset(ref)
  const key = recordKeyForFile(file)
  if (key) addRecordClosure(key, initialFiles, new Set(), false)
  else if (file.endsWith('.js')) initialFiles.add(file)
}

// The manifest's HTML entry is a fallback for unusual Vite HTML output where
// the script tag is rewritten without a matching manifest file record.
const htmlEntry = records.find(([, record]) => record.src === 'index.html' || (record.isEntry && record.name === 'app'))
if (initialFiles.size === 0 && htmlEntry) addRecordClosure(htmlEntry[0], initialFiles, new Set(), false)
if (initialFiles.size === 0) {
  console.error('check-bundle-size: index.html references no JavaScript entry script.')
  process.exit(1)
}
// Every page is a lazy route, so each has a manifest record keyed by its source
// path. Hashed chunk names change between builds; source paths do not.
/** @param {string} source */
function pageFiles(source) {
  if (!manifest[source]) {
    console.error(`check-bundle-size: ${source} has no chunk of its own; it must stay a lazy route.`)
    process.exit(1)
  }
  return addRecordClosure(source, new Set(), new Set(), true)
}

const shellFile = manifest['src/app/shell/AppShell.tsx']?.file
const sessionlessPages = [
  'src/features/links/routes/PublicSharePage.tsx',
  'src/features/auth/routes/LoginPage.tsx',
  'src/features/auth/routes/SetupPage.tsx',
  'src/features/emergency/routes/EmergencyPage.tsx'
]
const shellLeaks = sessionlessPages.filter((source) => shellFile && pageFiles(source).has(relativeAsset(shellFile)))

const publicFiles = pageFiles('src/features/links/routes/PublicSharePage.tsx')
const marginalFiles = [...publicFiles].filter((file) => !initialFiles.has(file) && file.endsWith('.js'))
const initialBytes = [...initialFiles]
  .filter((file) => file.endsWith('.js'))
  .reduce((sum, file) => sum + gzipSize(file), 0)
const shareBytes = marginalFiles.reduce((sum, file) => sum + gzipSize(file), 0)

/**
 * @param {string} name
 * @param {number} actual
 * @param {number} budget
 */
function report(name, actual, budget) {
  const ok = actual <= budget
  console.log(
    `${ok ? 'PASS' : 'FAIL'}  ${name}: ${(actual / 1024).toFixed(1)} KiB (budget ${(budget / 1024).toFixed(0)} KiB)`
  )
  return ok
}

const okInitial = report('Initial JS (gzip)', initialBytes, INITIAL_JS_BUDGET)
const okShare = report('Public-share JS (gzip, marginal)', shareBytes, SHARE_PAGE_JS_BUDGET)
for (const source of shellLeaks) console.log(`FAIL  ${source} loads the signed-in shell`)
if (!shellFile) console.log('FAIL  src/app/shell/AppShell.tsx has no chunk of its own')
if (!okInitial || !okShare || shellLeaks.length > 0 || !shellFile) {
  console.error('\ncheck-bundle-size: a check failed.')
  process.exit(1)
}
