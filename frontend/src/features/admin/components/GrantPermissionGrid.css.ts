import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 0,
  maxWidth: '100%',
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.sm,
  background: 'transparent'
})

export const head = style({
  display: 'grid',
  gridTemplateColumns: '1fr 72px 72px',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: `${vars.space.sm} ${vars.space.md}`,
  color: vars.color.text.secondary,
  background: vars.color.surface.container,
  ...typography('bodySmall'),
  '@media': {
    [media.compact]: {
      gridTemplateColumns: 'minmax(0, 1fr) 52px 52px',
      paddingInline: vars.space.sm
    }
  }
})

export const row = style({
  display: 'grid',
  gridTemplateColumns: '1fr 72px 72px',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: `${vars.space.sm} ${vars.space.md}`,
  background: 'transparent',
  selectors: {
    '& + &': {
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
    }
  },
  '@media': {
    [media.compact]: {
      gridTemplateColumns: 'minmax(0, 1fr) 52px 52px',
      paddingInline: vars.space.sm
    }
  }
})

export const cell = style({
  display: 'flex',
  justifyContent: 'center'
})
