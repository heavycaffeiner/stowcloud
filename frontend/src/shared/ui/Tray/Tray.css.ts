import { style } from '@vanilla-extract/css'
import { focusRing, typography, vars } from '@/shared/theme'

export const root = style({
  width: `min(360px, calc(100vw - 2 * ${vars.space.lg}))`,
  maxHeight: '60vh',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.lg,
  background: vars.color.surface.overlay,
  color: vars.color.text.primary,
  boxShadow: vars.elevation.lg
})

export const header = style({
  minHeight: vars.density.row,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  paddingInline: vars.space.lg
})

export const title = style([
  focusRing,
  {
    display: 'inline-flex',
    alignItems: 'center',
    gap: vars.space.sm,
    borderRadius: vars.radius.xs,
    color: 'inherit',
    ...typography('label')
  }
])

export const notice = style({
  margin: 0,
  paddingInline: vars.space.lg,
  color: vars.color.danger.solid,
  ...typography('bodySmall')
})

export const scroll = style({
  minHeight: 0,
  overflowY: 'auto'
})

export const list = style({
  listStyle: 'none',
  margin: 0,
  padding: `${vars.space.sm} ${vars.space.lg}`
})

export const item = style({
  paddingBlock: vars.space.md,
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
})

/** The item's name and its progress figures, on one line. */
export const row = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: vars.space.md,
  marginBottom: vars.space.xs
})

export const name = style({
  minWidth: 0,
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.xs,
  overflowWrap: 'anywhere',
  ...typography('body')
})

export const meta = style({
  flexShrink: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const controls = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.xs
})
