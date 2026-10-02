// Custom property name inside a var() reference, for element.style and getComputedStyle.
export function cssVarName(ref: string): string {
  const match = /^var\((--[\w-]+)\)$/.exec(ref)
  if (!match) throw new Error(`Not a CSS variable reference: ${ref}`)
  return match[1]
}
