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
  padding: vars.space.xl,
  borderRadius: vars.radius.xl,
  background: vars.color.surface.raised,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  boxShadow: vars.elevation.sm,
  animation: `${scaleUp} ${vars.motion.medium} ${vars.motion.easing}`
})

export const boot = style({
  minHeight: ['100vh', '100dvh'],
  display: 'grid',
  placeItems: 'center'
})
