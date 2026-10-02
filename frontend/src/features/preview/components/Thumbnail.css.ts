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
  bottom: '8px',
  right: '8px',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '24px',
  height: '24px',
  borderRadius: vars.radius.sm,
  background: 'rgb(0 0 0 / 65%)',
  color: '#fff',
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
