import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const row = style({
  minWidth: 0,
  '@media': {
    [media.compact]: {
      display: 'grid',
      gridTemplateColumns: 'auto minmax(0, 1fr)'
    }
  }
})

export const meta = style({
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const flag = style({
  flex: 'none',
  maxWidth: '100%',
  padding: `${vars.space.xs} ${vars.space.sm}`,
  borderRadius: vars.radius.full,
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft,
  ...typography('labelSmall'),
  overflowWrap: 'anywhere',
  '@media': {
    [media.compact]: {
      gridColumn: '2',
      justifySelf: 'start',
      maxWidth: '100%'
    }
  }
})

export const rowReadonly = style({
  cursor: 'default',
  opacity: 0.72
})

export const progress = style({
  flex: 'none',
  width: vars.space.xl,
  height: vars.space.xl
})
