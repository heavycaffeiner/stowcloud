import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.md,
  inlineSize: '100%',
  minBlockSize: '48px',
  padding: `${vars.space.sm} ${vars.space.lg}`,
  boxSizing: 'border-box',
  border: 'none',
  background: 'transparent',
  color: vars.color.text.primary,
  ...typography('body'),
  textAlign: 'start',
  cursor: 'pointer',
  selectors: { '&:hover': { background: vars.color.surface.container } }
})

export const label = style({
  color: vars.color.text.secondary
})
