import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm,
  minInlineSize: 0,
  width: 'min(420px, 80vw)',
  maxWidth: '100%'
})

export const nav = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm
})

export const here = style({
  margin: 0,
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary
})

export const body = style({
  minHeight: 0,
  minWidth: 0,
  maxHeight: '40vh',
  overflowY: 'auto',
  border: 'none',
  background: vars.color.surface.overlay,
  borderRadius: vars.radius.sm
})

export const status = style({
  margin: 0,
  padding: vars.space.lg,
  color: vars.color.text.secondary
})

export const entries = style({
  listStyle: 'none',
  margin: 0,
  padding: vars.space.xs
})

export const entry = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  width: '100%',
  minHeight: vars.density.control,
  padding: vars.space.sm,
  border: 'none',
  borderRadius: vars.radius.xs,
  background: 'none',
  color: 'inherit',
  textAlign: 'left',
  selectors: {
    'button&': {
      cursor: 'pointer'
    },
    'button&:hover': {
      background: vars.color.surface.raised
    }
  }
})

export const entryName = style({
  minWidth: 0,
  overflowWrap: 'anywhere'
})

export const entrySelected = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg
})

export const entryDisabled = style({
  color: vars.color.text.secondary,
  opacity: 0.6
})
