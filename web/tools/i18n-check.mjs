// CI gate for the catalogues in src/lib/i18n.
// Walks every `t('…')` call site and fails the build on any drift:
//
//   * a key missing from a catalogue: that language would render the raw key;
//   * a catalogue entry no call site uses: dead copy, or a renamed key;
//   * `{placeholder}` sets that disagree between languages: a dropped hole
//     renders as a missing word, an invented one renders literally;
//   * a `t()` argument that is not a dotted key at all: the fingerprint of
//     someone passing display text straight into `t()`.
//
// Strings that cross a thread or module boundary as data (the upload worker
// posts a key because it has no locale state, `job-tray.svelte.ts` stores one
// for `JobTray.svelte` to render) are not `t()` calls at their source. They
// are marked `/* i18n */` at the literal and collected here too.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join, relative } from 'node:path'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const SRC = join(root, 'src')
const I18N = join(SRC, 'lib', 'i18n')
const LOCALES = ['ko', 'en']

/** Mock fixtures and tests carry sample data, not UI copy. */
const SKIP = /\.test\.ts$|[\\/]mock(-seed)?\.ts$|[\\/]api[\\/]share\.ts$|[\\/]lib[\\/]i18n[\\/]/

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(svelte|ts)$/.test(p) && !SKIP.test(p)) out.push(p)
  }
  return out
}

// Literal-only by design: a key built at runtime cannot be extracted, and
// silently missing one is worse than not allowing it.
const CALL = /(?:^|[^\w.$])t\(\s*(['"])((?:\\.|(?!\1)[^\\])*)\1/g
const DEFERRED = /\/\* i18n \*\/\s*(['"])((?:\\.|(?!\1)[^\\])*)\1/g
const KEY_SHAPE = /^[a-z][a-z0-9_]*\.[a-z0-9_]+$/

const used = new Map() // key -> first "file:line"

for (const file of walk(SRC)) {
  const text = readFileSync(file, 'utf8')
  for (const re of [CALL, DEFERRED]) {
    re.lastIndex = 0
    for (const m of text.matchAll(re)) {
      const key = m[2].replace(/\\(['"\\])/g, '$1')
      if (used.has(key)) continue
      const line = text.slice(0, m.index).split('\n').length
      used.set(key, `${relative(root, file).replace(/\\/g, '/')}:${line}`)
    }
  }
}

const catalogue = Object.fromEntries(LOCALES.map((l) => [l, JSON.parse(readFileSync(join(I18N, `${l}.json`), 'utf8'))]))
const holes = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join()

const malformed = []
const missing = []
const orphaned = []
const mismatched = []

for (const [key, where] of used) {
  if (!KEY_SHAPE.test(key)) {
    malformed.push(`  ${where}  ${JSON.stringify(key)}`)
    continue
  }
  for (const l of LOCALES) if (!(key in catalogue[l])) missing.push(`  ${where}  ${key}  [${l}]`)
  const present = LOCALES.filter((l) => key in catalogue[l])
  if (present.length === LOCALES.length && new Set(present.map((l) => holes(catalogue[l][key]))).size > 1)
    mismatched.push(`  ${where}  ${key}  ` + present.map((l) => `${l}: ${JSON.stringify(catalogue[l][key])}`).join(' | '))
}
for (const l of LOCALES) for (const key of Object.keys(catalogue[l])) if (!used.has(key)) orphaned.push(`  ${key}  [${l}]`)

const report = (title, lines) => {
  if (lines.length === 0) return
  console.error(`\n${title} (${lines.length}):`)
  for (const l of lines) console.error(l)
}

report('Not a catalogue key: display text passed to t()?', malformed)
report('Key missing from a catalogue', missing)
report('Placeholders disagree between languages', mismatched)
report('Catalogue entry with no call site', orphaned)

if (malformed.length || missing.length || orphaned.length || mismatched.length) {
  console.error(`\ni18n-check failed. ${used.size} keys in use.`)
  process.exit(1)
}
console.log(`i18n-check: ${used.size} keys, ${LOCALES.length} locales, no drift.`)
