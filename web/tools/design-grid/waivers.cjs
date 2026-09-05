// Loads, validates and tracks web/design-waivers.json.
//
// The design gate fails hard, so this file is the only way past it. Two rules
// keep it from turning into a mute button: every entry needs a reason and an
// expiry, and an entry that matched nothing during a run is reported as dead
// and fails the build. A waiver that stops being true stops being accepted.

const fs = require('node:fs')
const path = require('node:path')
const { checksFor } = require('./policy.cjs')

const WAIVERS_PATH = path.join(__dirname, '..', '..', 'design-waivers.json')

const LAYERS = ['static', 'component', 'runtime']
const MIN_REASON_LENGTH = 30
const KNOWN_FIELDS = new Set(['id', 'layer', 'check', 'selector', 'subtree', 'reason', 'expires'])

class WaiverConfigError extends Error {
  constructor(message) {
    super(message)
    this.name = 'WaiverConfigError'
  }
}

const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/

function isRealDate(s) {
  const m = DATE_RE.exec(s)
  if (!m) return false
  const [, y, mo, d] = m.map(Number)
  const t = new Date(Date.UTC(y, mo - 1, d))
  return t.getUTCFullYear() === y && t.getUTCMonth() === mo - 1 && t.getUTCDate() === d
}

/**
 * Reads and validates the waiver file.
 * @param {string} today YYYY-MM-DD, injected so expiry is testable
 * @param {string} [file] override, for tests
 * @returns {{list: Array, used: Set<string>, path: string}}
 */
function loadWaivers(today, file = WAIVERS_PATH) {
  if (!isRealDate(today)) {
    throw new WaiverConfigError(`loadWaivers: "today" must be a real YYYY-MM-DD date, got ${today}`)
  }

  let raw
  try {
    raw = fs.readFileSync(file, 'utf8')
  } catch (e) {
    if (e.code === 'ENOENT') return { list: [], used: new Set(), path: file }
    throw new WaiverConfigError(`${file}: ${e.message}`)
  }

  let doc
  try {
    doc = JSON.parse(raw)
  } catch (e) {
    throw new WaiverConfigError(`${file}: not valid JSON (${e.message})`)
  }

  if (doc === null || typeof doc !== 'object' || Array.isArray(doc) || !Array.isArray(doc.waivers)) {
    throw new WaiverConfigError(`${file}: expected an object with a "waivers" array`)
  }

  const seen = new Set()
  const list = doc.waivers.map((w, i) => validate(w, i, seen, today, file))
  return { list, used: new Set(), path: file }
}

function validate(w, i, seen, today, file) {
  const at = `${file}: waivers[${i}]`

  if (w === null || typeof w !== 'object' || Array.isArray(w)) {
    throw new WaiverConfigError(`${at}: expected an object`)
  }
  for (const k of Object.keys(w)) {
    if (!KNOWN_FIELDS.has(k)) {
      throw new WaiverConfigError(
        `${at}: unknown field "${k}" (allowed: ${[...KNOWN_FIELDS].join(', ')})`
      )
    }
  }

  const { id, layer, check, selector, reason, expires } = w
  const subtree = w.subtree ?? false

  if (typeof id !== 'string' || id.trim() === '') {
    throw new WaiverConfigError(`${at}: "id" must be a non-empty string`)
  }
  if (seen.has(id)) throw new WaiverConfigError(`${at}: duplicate id "${id}"`)
  seen.add(id)

  if (!LAYERS.includes(layer)) {
    throw new WaiverConfigError(`${at} (${id}): "layer" must be one of ${LAYERS.join(', ')}`)
  }

  const allowed = checksFor(layer)
  if (typeof check !== 'string' || (check !== '*' && !allowed.includes(check))) {
    throw new WaiverConfigError(
      `${at} (${id}): "check" must be "*" or one the ${layer} layer emits (${allowed.join(', ')}), got ${JSON.stringify(check)}`
    )
  }

  if (typeof selector !== 'string' || selector.trim() === '') {
    throw new WaiverConfigError(`${at} (${id}): "selector" must be a non-empty string`)
  }

  if (typeof subtree !== 'boolean') {
    throw new WaiverConfigError(`${at} (${id}): "subtree" must be a boolean`)
  }
  if (subtree && layer !== 'runtime') {
    throw new WaiverConfigError(`${at} (${id}): "subtree" applies to the runtime layer only`)
  }

  if (typeof reason !== 'string' || reason.trim().length < MIN_REASON_LENGTH) {
    throw new WaiverConfigError(
      `${at} (${id}): "reason" must be at least ${MIN_REASON_LENGTH} characters and say why this cannot be fixed`
    )
  }

  if (typeof expires !== 'string' || !isRealDate(expires)) {
    throw new WaiverConfigError(`${at} (${id}): "expires" must be a real YYYY-MM-DD date`)
  }
  if (expires < today) {
    throw new WaiverConfigError(`${at} (${id}): expired on ${expires}; re-justify it or fix the violation`)
  }

  return { id, layer, check, selector, subtree, reason, expires }
}

/** Today in the local calendar, as YYYY-MM-DD. */
function todayString(now = new Date()) {
  const p = (n) => String(n).padStart(2, '0')
  return `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`
}

let shared = null

/**
 * One waiver set per process, so the stylelint plugin's suppressions and the
 * orchestrator's dead-waiver sweep see the same `used` bookkeeping. Without
 * this the plugin would mark a waiver used in a set nobody else holds, and
 * every static waiver would be reported dead.
 */
function sharedWaivers() {
  if (!shared) shared = loadWaivers(todayString())
  return shared
}

/** Waivers belonging to one layer, in file order. */
function waiversFor(set, layer) {
  return set.list.filter((w) => w.layer === layer)
}

function markUsed(set, id) {
  set.used.add(id)
}

/**
 * True when `violation` is covered, marking the covering waiver used.
 *
 * Callers that can run a CSS selector match themselves (the in-page collector,
 * the jsdom component tests) resolve the match and put the waiver id on
 * `violation.waivedBy`; this only verifies and records it. The static layer has
 * no DOM, so its selectors are matched literally.
 */
function isWaived(set, violation) {
  if (violation.waivedBy) {
    const w = set.list.find((x) => x.id === violation.waivedBy)
    if (!w) throw new WaiverConfigError(`violation claims unknown waiver id "${violation.waivedBy}"`)
    if (w.layer !== violation.layer || (w.check !== '*' && w.check !== violation.check)) {
      throw new WaiverConfigError(
        `waiver "${w.id}" (${w.layer}/${w.check}) cannot cover a ${violation.layer}/${violation.check} violation`
      )
    }
    markUsed(set, w.id)
    return true
  }

  const w = set.list.find(
    (x) =>
      x.layer === violation.layer &&
      (x.check === '*' || x.check === violation.check) &&
      x.selector === violation.selector
  )
  if (!w) return false
  markUsed(set, w.id)
  return true
}

/**
 * Waivers never matched during the run, scoped to the layers that actually ran.
 * Passing a partial layer list would report a waiver as dead only because its
 * layer did not execute, so the caller passes what it ran.
 */
function deadWaivers(set, ranLayers) {
  return set.list.filter((w) => ranLayers.includes(w.layer) && !set.used.has(w.id))
}

module.exports = {
  WAIVERS_PATH,
  LAYERS,
  MIN_REASON_LENGTH,
  WaiverConfigError,
  loadWaivers,
  todayString,
  sharedWaivers,
  waiversFor,
  markUsed,
  isWaived,
  deadWaivers
}
