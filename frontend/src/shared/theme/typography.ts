import { vars } from './contract.css'

export type TypeRole = keyof typeof vars.typography

/** The four font properties of one type role, to spread into a style. */
export const typography = (role: TypeRole) => ({
  fontSize: vars.typography[role].size,
  fontWeight: vars.typography[role].weight,
  lineHeight: vars.typography[role].lineHeight,
  letterSpacing: vars.typography[role].tracking
})
