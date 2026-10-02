import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const wrap = style({
  position: 'relative',
  width: '100%',
  height: '100%',
  overflow: 'hidden'
})

export const img = style({
  width: '100%',
  height: '100%',
  objectFit: 'cover',
  display: 'block'
})

export const badge = style({
  position: 'absolute',
  bottom: vars.space.sm,
  right: vars.space.sm,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.space.xl,
  height: vars.space.xl,
  borderRadius: vars.radius.sm,
  background: `color-mix(in srgb, ${vars.color.surface.inverse} 85%, transparent)`,
  color: vars.color.text.inverse,
  pointerEvents: 'none'
})

export const icon = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '100%',
  height: '100%',
  color: vars.color.text.secondary
})
