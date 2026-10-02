import { style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

export const toolbar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  padding: `${vars.space.sm} ${vars.space.lg}`,
  marginBottom: vars.space.sm,
  borderRadius: vars.radius.md,
  background: vars.color.surface.container,
  '@media': {
    [media.compact]: {
      paddingInline: vars.space.md
    }
  }
})

export const selectAll = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minHeight: vars.density.controlDesktop
})

export const toolbarActions = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.xs,
  '@media': {
    [media.compact]: {
      width: '100%',
      justifyContent: 'flex-end'
    }
  }
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  padding: `${vars.space.sm} ${vars.space.lg}`,
  minWidth: 0,
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  '@media': {
    [media.compact]: {
      gap: vars.space.sm,
      paddingInline: vars.space.sm
    }
  }
})

export const notice = style({
  display: 'grid',
  gridTemplateColumns: 'minmax(0, 1fr) auto',
  alignItems: 'center',
  gap: vars.space.sm,
  margin: `0 0 ${vars.space.sm}`,
  padding: `${vars.space.sm} ${vars.space.md} ${vars.space.sm} ${vars.space.lg}`,
  overflowWrap: 'anywhere',
  borderRadius: vars.radius.sm,
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})
