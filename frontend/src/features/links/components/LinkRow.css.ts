import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const row = style({
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      display: 'grid',
      gridTemplateColumns: 'auto minmax(0, 1fr)'
    }
  }
})

export const meta = style({
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  lineHeight: vars.typography.bodySmall.lineHeight,
  letterSpacing: vars.typography.bodySmall.tracking
})

export const flag = style({
  flex: 'none',
  maxWidth: '100%',
  padding: '4px 8px',
  borderRadius: vars.radius.full,
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft,
  fontSize: vars.typography.labelSmall.size,
  fontWeight: vars.typography.labelSmall.weight,
  lineHeight: vars.typography.labelSmall.lineHeight,
  letterSpacing: vars.typography.labelSmall.tracking,
  overflowWrap: 'anywhere',
  '@media': {
    '(max-width: 599.98px)': {
      gridColumn: '2',
      justifySelf: 'start',
      maxWidth: '100%'
    }
  }
})

export const rowReadonly = style({
  cursor: 'default',
  opacity: '0.72'
})

export const progress = style({
  flex: 'none',
  width: '24px',
  height: '24px'
})
