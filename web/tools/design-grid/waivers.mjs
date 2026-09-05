// ESM face of waivers.cjs. See policy.mjs.

import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const impl = require('./waivers.cjs')

export const WAIVERS_PATH = impl.WAIVERS_PATH
export const LAYERS = impl.LAYERS
export const MIN_REASON_LENGTH = impl.MIN_REASON_LENGTH
export const WaiverConfigError = impl.WaiverConfigError
export const loadWaivers = impl.loadWaivers
export const todayString = impl.todayString
export const sharedWaivers = impl.sharedWaivers
export const waiversFor = impl.waiversFor
export const markUsed = impl.markUsed
export const isWaived = impl.isWaived
export const deadWaivers = impl.deadWaivers
