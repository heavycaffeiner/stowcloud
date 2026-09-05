// Advisory-only client-side
// feedback while typing a new password (settings' PasswordSection, the
// first-run /setup form, and admin user creation all want the same signal).
//
// This is a length + character-class heuristic, not zxcvbn or any other
// dictionary/pattern-aware scorer, deliberately: the actual floor is
// `auth.weak_password` enforced server-side (sc-auth, currently a bare
// minimum length), and this bar exists only to nudge a user past that floor
// before they submit, not to replace the server's judgment. Pulling in a
// real strength-estimation library for a hint bar felt like the wrong trade.

import { t } from '../i18n'
export type PasswordStrengthTier = 'weak' | 'fair' | 'strong'

export interface PasswordStrengthResult {
  /** 0..7: length tier (0..3) + character-class variety (0..4). */
  score: number
  tier: PasswordStrengthTier
  /** 0..1, for a progress bar. */
  ratio: number
  /** Korean label for the tier, for display next to the bar. */
  label: string
}

const MAX_SCORE = 7

function lengthTier(len: number): number {
  if (len < 10) return 0
  if (len < 14) return 1
  if (len < 20) return 2
  return 3
}

function classVariety(pw: string): number {
  let classes = 0
  if (/[a-z]/.test(pw)) classes++
  if (/[A-Z]/.test(pw)) classes++
  if (/[0-9]/.test(pw)) classes++
  if (/[^A-Za-z0-9]/.test(pw)) classes++
  return classes
}

export function scorePasswordStrength(password: string): PasswordStrengthResult {
  if (password.length === 0) {
    return { score: 0, tier: 'weak', ratio: 0, label: '' }
  }
  const score = lengthTier(password.length) + classVariety(password)
  const tier: PasswordStrengthTier = score <= 2 ? 'weak' : score <= 4 ? 'fair' : 'strong'
  const label = tier === 'weak' ? t('password.weak') : tier === 'fair' ? t('common.fair') : t('password.strong')
  return { score, tier, ratio: score / MAX_SCORE, label }
}
