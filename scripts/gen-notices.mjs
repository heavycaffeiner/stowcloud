#!/usr/bin/env node
// Regenerate THIRD-PARTY-NOTICES.md from the two dependency trees that end up
// inside the shipped binary. Run from the repo root:
//
//   node scripts/gen-notices.mjs
//
// Hand-maintaining this list is not possible: tens of Go modules and npm
// packages, each with its own copyright line, and MIT/BSD/ISC all require the
// notice to travel with the copy. The file is generated so it can be true.
//
// Scope. Go: the modules owning packages the shipped binary actually links,
// asked of the toolchain with the tags the image builds with, so a module that
// only serves tests or another build never appears. npm: only what the browser
// downloads, which is not the same as `dependencies`. Vite compiles with
// rollup/esbuild/postcss/the svelte compiler and ships none of them, so the
// shipped set is listed explicitly below rather than derived.

import { readFileSync, readdirSync, existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'

const OUT = 'THIRD-PARTY-NOTICES.md'
const NM = 'web/node_modules'
// The tags the image builds with. Without them the graph is a different
// binary's, and the notice would describe something nobody ships.
const BUILD_TAGS = 'embed_ui compat_nc'

// Packages whose code or data reaches the browser. Everything else in
// `web/package.json` is a build tool.
const JS_RUNTIME = [
  'svelte', '@sveltejs/kit', 'esm-env', 'clsx', 'devalue',
  'm3-svelte', '@ktibow/iconset-material-symbols',
  '@ktibow/material-color-utilities-nightly',
  '@fontsource-variable/google-sans-flex',
  'codemirror', '@marijn/find-cluster-break', 'style-mod', 'w3c-keyname', 'crelt',
]
for (const scope of ['@codemirror', '@lezer']) {
  for (const n of readdirSync(join(NM, scope))) JS_RUNTIME.push(`${scope}/${n}`)
}

const LICENSE_FILE = /^(licen[sc]e|copying|notice|unlicense)/i

/** All licence-ish files in a directory, as {name, text}. */
function licenseTexts(dir) {
  if (!existsSync(dir)) return []
  return readdirSync(dir)
    .filter((n) => LICENSE_FILE.test(n) && !/\.(js|ts|mjs|cjs|d\.ts)$/i.test(n))
    .sort()
    .map((n) => {
      try {
        return { name: n, text: readFileSync(join(dir, n), 'utf8').replace(/\r\n/g, '\n').trim() }
      } catch {
        return null
      }
    })
    .filter(Boolean)
}

// Identical texts are pooled and printed once; a Rust workspace pulls in
// hundreds of byte-identical MIT copies and reprinting each is noise, not
// compliance.
const pool = new Map() // sha1 -> {id, text, users:[]}
function intern(text, user) {
  const h = createHash('sha1').update(text).digest('hex')
  let e = pool.get(h)
  if (!e) { e = { id: pool.size + 1, text, users: [] }; pool.set(h, e) }
  e.users.push(user)
  return e.id
}

const rows = [] // {eco, name, version, license, url, texts:[id]}

// --- Go --------------------------------------------------------------------
// The linked set, not the module requirement list: `go list -deps` answers
// which packages the binary actually pulls in, and the module each belongs to.
// A module required by go.mod but linked by nothing is not redistributed and
// is not listed.
const goList = execFileSync('go', [
  'list', '-deps', '-tags', BUILD_TAGS,
  '-f', '{{if .Module}}{{.Module.Path}}\t{{.Module.Version}}\t{{.Module.Dir}}{{end}}',
  './cmd/sc-engine',
], { cwd: 'go', encoding: 'utf8', env: { ...process.env, CGO_ENABLED: '0' } })

const OWN = 'github.com/heavycaffeiner/stowcloud/go'
const modules = new Map()
for (const line of goList.split('\n')) {
  if (!line.trim()) continue
  const [path, version, dir] = line.split('\t')
  if (!path || path === OWN) continue
  modules.set(path, { version: version || '(main)', dir: dir || '' })
}

for (const [path, { version, dir }] of [...modules].sort((a, b) => a[0].localeCompare(b[0]))) {
  if (!dir) throw new Error(`${path}: no module directory; run go mod download first`)
  const files = licenseTexts(dir)
  rows.push({
    eco: 'go',
    name: path,
    version,
    // Go declares no licence field, so the shipped text is the only
    // statement of terms and its absence is reported rather than guessed.
    license: files.length ? 'see text' : 'NOT DECLARED',
    url: `https://pkg.go.dev/${path}`,
    texts: files.map((f) => intern(f.text, `${path} ${version}`)),
  })
}

// --- npm -------------------------------------------------------------------
for (const name of JS_RUNTIME.sort()) {
  const dir = join(NM, name)
  const pj = join(dir, 'package.json')
  if (!existsSync(pj)) throw new Error(`${name}: not installed, run pnpm install in web/ first`)
  const j = JSON.parse(readFileSync(pj, 'utf8'))
  const files = licenseTexts(dir)
  const repo = typeof j.repository === 'string' ? j.repository : j.repository?.url || ''
  rows.push({
    eco: 'npm',
    name,
    version: j.version,
    license: j.license || 'NOT DECLARED',
    url: repo.replace(/^git\+/, '').replace(/\.git$/, '') || `https://www.npmjs.com/package/${name}`,
    texts: files.map((f) => intern(f.text, `${name} ${j.version}`)),
  })
}

// --- emit ------------------------------------------------------------------
const undeclared = rows.filter((r) => !r.texts.length)
const out = []
out.push('# Third-party notices')
out.push('')
out.push('Stowcloud ships as one binary. The Go dependency graph is linked into')
out.push('it and the built frontend is embedded by `//go:embed` at compile time, so')
out.push('everything listed here is *redistributed*, not merely used at build time.')
out.push('MIT, BSD, ISC and Apache-2.0 all require their notice to travel with the')
out.push('copy; this file is that notice.')
out.push('')
out.push('Generated by `scripts/gen-notices.mjs`. Do not edit by hand: regenerate it')
out.push('when a dependency is added, removed, or bumped.')
out.push('')
out.push(`Components: ${rows.filter((r) => r.eco === 'go').length} Go modules, ` +
         `${rows.filter((r) => r.eco === 'npm').length} npm packages. ` +
         `Distinct licence texts: ${pool.size}.`)
out.push('')
if (undeclared.length) {
  out.push('> **No licence file shipped in the package.** Whatever terms the')
  out.push('> project states elsewhere are the only statement: ' +
           undeclared.map((r) => `\`${r.name} ${r.version}\``).join(', ') + '.')
  out.push('')
}
out.push('Stowcloud itself is AGPL-3.0-or-later; see [`LICENSE`](LICENSE).')
out.push('')
out.push('---')
out.push('')

for (const [eco, title] of [['go', 'Go modules (linked into the binary)'], ['npm', 'npm packages (embedded frontend)']]) {
  out.push(`## ${title}`)
  out.push('')
  out.push('| Package | Version | Licence | Texts |')
  out.push('|---|---|---|---|')
  for (const r of rows.filter((x) => x.eco === eco)) {
    const t = r.texts.length ? r.texts.map((i) => `[#${i}](#licence-text-${i})`).join(', ') : 'none'
    out.push(`| [${r.name}](${r.url}) | ${r.version} | ${r.license} | ${t} |`)
  }
  out.push('')
}

out.push('---')
out.push('')
out.push('## Licence texts')
out.push('')
for (const e of [...pool.values()].sort((a, b) => a.id - b.id)) {
  out.push(`### Licence text ${e.id}`)
  out.push('')
  out.push(`Applies to: ${e.users.map((u) => `\`${u}\``).join(', ')}`)
  out.push('')
  out.push('```')
  out.push(e.text.replace(/```/g, "'''"))
  out.push('```')
  out.push('')
}

writeFileSync(OUT, out.join('\n'))
console.log(`${OUT}: ${rows.length} components, ${pool.size} licence texts, ${out.join('\n').length} bytes`)
