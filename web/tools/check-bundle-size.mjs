// Enforces the two byte budgets for the browser payload: initial JavaScript gzip
// is at most 256 KiB and the public-share route adds at most 60 KiB marginal.
// The Svelte-era initial limit was 150 KiB; the React 19, React Router 7,
// TanStack Query and mdui boot graph measured 225.7 KiB gzip after production
// router resolution and static color tokens, so 256 KiB leaves about 13% headroom.
// Run after `pnpm build`; Vite's manifest is the source of truth for emitted chunks.

import { gzipSync } from 'node:zlib'
import { readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const webRoot = path.dirname(path.dirname(fileURLToPath(import.meta.url)))
const buildDir = path.resolve(webRoot, '../go/engine/http/spa/build')
const manifestPath = path.join(buildDir, '.vite', 'manifest.json')
const indexHtmlPath = path.join(buildDir, 'index.html')

const INITIAL_JS_BUDGET = 256 * 1024
// Measured 225.7 KiB initial and 7.6 KiB public-share marginal on 2026-09-12.
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
const byFile = new Map(records
  .filter(([, value]) => typeof value.file === 'string')
  .map(([key, value]) => [value.file, { key, ...value }]))

function relativeAsset(value) {
  return value.replace(/^\//, '').replace(/^\.\//, '')
}

function gzipSize(file) {
  return gzipSync(readFileSync(path.join(buildDir, relativeAsset(file))), { level: 9 }).length
}

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
// React Router's lazy route module is represented by its source key when the
// route is split. Do not depend on a framework-generated route id: Vite keeps
// the source pathname stable even as chunk names and hashes change.
const publicRecord = records.find(([key, record]) => {
  const normalizedKey = key.replaceAll('\\\\', '/')
  return normalizedKey.endsWith('/PublicSharePage.tsx') || normalizedKey.endsWith('/PublicSharePage.ts') ||
    normalizedKey.endsWith('/PublicSharePage.jsx') || normalizedKey.endsWith('/PublicSharePage.js') ||
    `${normalizedKey} ${record.src ?? ''} ${record.name ?? ''}`.toLowerCase().includes('public-share')
})

// If React routes are in the single app entry, the public share page has no
// marginal chunk. When a future build splits it, follow its manifest imports
// and dynamic imports so the budget covers exactly the route's reachable code.
const publicFiles = publicRecord
  ? addRecordClosure(publicRecord[0], new Set(), new Set(), true)
  : new Set(initialFiles)
const marginalFiles = [...publicFiles].filter((file) => !initialFiles.has(file) && file.endsWith('.js'))
const initialBytes = [...initialFiles].filter((file) => file.endsWith('.js')).reduce((sum, file) => sum + gzipSize(file), 0)
const shareBytes = marginalFiles.reduce((sum, file) => sum + gzipSize(file), 0)

function report(name, actual, budget) {
  const ok = actual <= budget
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}: ${(actual / 1024).toFixed(1)} KiB (budget ${(budget / 1024).toFixed(0)} KiB)`)
  return ok
}

const okInitial = report('Initial JS (gzip)', initialBytes, INITIAL_JS_BUDGET)
const okShare = report('Public-share JS (gzip, marginal)', shareBytes, SHARE_PAGE_JS_BUDGET)
if (!okInitial || !okShare) {
  console.error('\ncheck-bundle-size: a budget was exceeded.')
  process.exit(1)
}
