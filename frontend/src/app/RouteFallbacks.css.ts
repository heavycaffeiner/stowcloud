import { style } from '@vanilla-extract/css'
import { scaleUp, vars } from '../ui/theme.css'

export const root = style({
  minHeight: '100dvh',
  display: 'grid',
  placeItems: 'center',
  padding: vars.layout.pagePad,
  background: vars.surface.page
})

export const embedded = style({
  minHeight: '100%',
  background: 'transparent'
})

export const card = style({
  width: 'min(100%, 34rem)',
  padding: '24px',
  borderRadius: vars.radius.auth,
  background: vars.surface.raised,
  border: `1px solid ${vars.outline.variant}`,
  boxShadow: vars.shadow.level1,
  animation: `${scaleUp} 220ms cubic-bezier(0.2, 0, 0, 1)`
})

export const boot = style({
  minHeight: ['100vh', '100dvh'],
  display: 'grid',
  placeItems: 'center'
})
