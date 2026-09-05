// The one implementation of the grid policy.
// policy.mjs re-exports this through createRequire rather than restating it,
// because a static rule and a runtime audit that disagree about what is legal
// are worse than no check at all.

const fs = require('node:fs')
const path = require('node:path')

const POLICY_PATH = path.join(__dirname, 'policy.json')

let cached = null

/** Loads policy.json once and returns it frozen. */
function loadPolicy() {
  if (cached) return cached
  const raw = JSON.parse(fs.readFileSync(POLICY_PATH, 'utf8'))
  cached = deepFreeze(raw)
  return cached
}

function deepFreeze(o) {
  if (o && typeof o === 'object' && !Object.isFrozen(o)) {
    Object.freeze(o)
    for (const v of Object.values(o)) deepFreeze(v)
  }
  return o
}

let classIndex = null

function propertyIndex() {
  if (classIndex) return classIndex
  const p = loadPolicy()
  classIndex = new Map()
  for (const prop of p.spacingProperties) classIndex.set(prop, 'spacing')
  for (const prop of p.sizingProperties) classIndex.set(prop, 'sizing')
  for (const prop of p.hairlineProperties) classIndex.set(prop, 'hairline')
  for (const prop of p.radiusProperties) classIndex.set(prop, 'radius')
  for (const prop of p.typographyProperties) classIndex.set(prop, 'typography')
  return classIndex
}

/**
 * Property class used to select a predicate.
 * Returns null for properties the toolchain does not check.
 */
function classifyProperty(prop) {
  return propertyIndex().get(String(prop).trim().toLowerCase()) ?? null
}

/**
 * True when `px` is acceptable for the given class. Sign is ignored: a
 * negative offset (an inward focus ring, a nudge) follows the same rules as
 * its positive counterpart.
 */
function isAllowed(px, cls) {
  const p = loadPolicy()
  const a = Math.abs(px)
  if (!Number.isFinite(a)) return false
  switch (cls) {
    case 'spacing':
      return p.spacingScale.some((s) => Math.abs(a - s) <= p.tolerancePx)
    case 'sizing':
    case 'hairline':
      return p.hairlineExemptPx.includes(a) || a % p.gridUnit === 0
    case 'radius':
      return a === p.pillRadiusPx || p.hairlineExemptPx.includes(a) || a % p.gridUnit === 0
    case 'typography':
      return a % p.gridUnit === 0
    default:
      return true
  }
}

/** True when `v` lands on the grid within the policy tolerance. */
function onGrid(v) {
  const p = loadPolicy()
  if (!Number.isFinite(v)) return false
  return Math.abs(v - p.gridUnit * Math.round(v / p.gridUnit)) <= p.tolerancePx
}

/** True when `v` is a member of the spacing scale within the policy tolerance. */
function onScale(v) {
  const p = loadPolicy()
  if (!Number.isFinite(v)) return false
  return p.spacingScale.some((s) => Math.abs(v - s) <= p.tolerancePx)
}

/** Check names the given layer is allowed to emit. */
function checksFor(layer) {
  return loadPolicy().checks[layer] ?? null
}

module.exports = {
  POLICY_PATH,
  loadPolicy,
  classifyProperty,
  isAllowed,
  onGrid,
  onScale,
  checksFor
}
