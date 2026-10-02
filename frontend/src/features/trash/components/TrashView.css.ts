import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const checkbox = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minHeight: vars.density.controlDesktop,
  justifyContent: 'center',
  minWidth: vars.density.controlDesktop
})

export const rowActions = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.xs
})

export const name = style({
  flex: 1,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const meta = style({
  flexShrink: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  whiteSpace: 'nowrap',
  '@media': {
    [media.compact]: {
      display: 'none'
    }
  }
})

export const operation = style({
  display: 'grid',
  gap: vars.space.sm,
  margin: `${vars.space.md} 0`,
  padding: `${vars.space.md} ${vars.space.lg}`,
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.raised,
  boxShadow: vars.elevation.sm
})

export const operationHeading = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.sm,
  minWidth: 0
})

export const operationTitle = style({
  minWidth: 0,
  margin: 0,
  overflowWrap: 'anywhere',
  ...typography('title')
})

export const operationList = style({
  display: 'grid',
  gap: vars.space.xs,
  margin: 0,
  padding: 0,
  listStyle: 'none'
})

export const operationItem = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: vars.space.md,
  overflowWrap: 'anywhere',
  ...typography('bodySmall')
})

export const operationPath = style({
  minWidth: 0,
  overflowWrap: 'anywhere'
})

export const operationResult = style({
  flex: 'none'
})

export const operationError = style({
  color: vars.color.danger.solid
})
