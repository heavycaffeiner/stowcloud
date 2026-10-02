import { style } from '@vanilla-extract/css'
import { RESULT_ROW_HEIGHT } from '../logic/search-selectors'
import { focusOutline, media, typography, vars } from '@/shared/theme'

export const results = style({
  position: 'relative',
  maxHeight: '420px',
  overflowY: 'auto',
  overflowX: 'hidden',
  borderRadius: vars.radius.md,
  background: vars.color.surface.page,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  outline: 'none',
  '@media': {
    [media.compact]: {
      maxHeight: 'none',
      minHeight: '240px'
    }
  }
})

export const note = style({
  padding: `${vars.space.xxl} ${vars.space.lg}`,
  textAlign: 'center',
  color: vars.color.text.secondary,
  ...typography('body'),
  margin: 0
})

export const spacer = style({
  position: 'relative'
})

export const rows = style({
  listStyle: 'none',
  margin: 0,
  padding: 0,
  width: '100%'
})

export const row = style({
  width: '100%',
  height: `${RESULT_ROW_HEIGHT}px`,
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  padding: `0 ${vars.space.lg}`,
  border: 'none',
  background: 'transparent',
  color: vars.color.text.primary,
  cursor: 'pointer',
  textAlign: 'left',
  boxSizing: 'border-box',
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 5%, transparent)`
    },
    '&:focus-visible': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 8%, transparent)`,
      ...focusOutline,
      outlineOffset: vars.focusRing.insetOffset
    }
  },
  '@media': {
    [media.compact]: {
      paddingInline: vars.space.md,
      gap: vars.space.sm
    }
  }
})

export const rowIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.space.xxl,
  height: vars.space.xxl,
  borderRadius: vars.radius.sm,
  background: `color-mix(in srgb, ${vars.color.text.primary} 6%, transparent)`,
  flex: 'none'
})

export const text = style({
  flex: 1,
  minWidth: 0,
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xxs
})

export const name = style({
  ...typography('bodyLarge'),
  fontWeight: vars.font.weight.medium,
  color: vars.color.text.primary,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const folder = style({
  ...typography('bodySmall'),
  color: vars.color.text.secondary,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const cell = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  gap: vars.space.xxs,
  flex: 'none',
  '@media': {
    [media.compact]: {
      display: 'none'
    }
  }
})

export const size = style({
  ...typography('bodySmall'),
  fontWeight: vars.font.weight.medium,
  color: vars.color.text.primary
})

export const date = style({
  ...typography('bodySmall'),
  color: vars.color.text.secondary
})
