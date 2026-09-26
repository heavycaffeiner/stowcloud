// What is wrong with a password-change form, before it reaches the network.
//
// A descriptor rather than a sentence: the screen owns the wording, in the
// reader's language, and the minimum is a placeholder in it.
export type PasswordProblem = { readonly kind: 'too_short'; readonly min: number } | { readonly kind: 'mismatch' }

export function validatePasswordChange(next: string, confirm: string, minLength = 10): PasswordProblem | null {
  if (next.length < minLength) return { kind: 'too_short', min: minLength }
  if (next !== confirm) return { kind: 'mismatch' }
  return null
}
