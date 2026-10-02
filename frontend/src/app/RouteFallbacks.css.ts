import { style } from '@vanilla-extract/css'
import { scaleUp, vars } from '@/shared/theme'

export const root = style({
  minHeight: '100dvh',
  display: 'grid',
  placeItems: 'center',
  padding: vars.layout.pagePad,
  background: vars.color.surface.page
})

export const embedded = style({
  minHeight: '100%',
  background: 'transparent'
})

export const card = style({
  width: 'min(100%, 34rem)',
  padding: '24px',
  borderRadius: vars.radius.xl,
  background: vars.color.surface.raised,
  border: `1px solid ${vars.color.border.subtle}`,
  boxShadow: vars.elevation.sm,
  animation: `${scaleUp} 220ms cubic-bezier(0.2, 0, 0, 1)`
})

export const boot = style({
  minHeight: ['100vh', '100dvh'],
  display: 'grid',
  placeItems: 'center'
})
