// ESM face of policy.cjs. No logic of its own:
// the audit driver and the component tests must answer "is this legal?" the
// same way the stylelint plugin does.

import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const impl = require('./policy.cjs')

export const POLICY_PATH = impl.POLICY_PATH
export const loadPolicy = impl.loadPolicy
export const classifyProperty = impl.classifyProperty
export const isAllowed = impl.isAllowed
export const onGrid = impl.onGrid
export const onScale = impl.onScale
export const checksFor = impl.checksFor
