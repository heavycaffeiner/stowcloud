import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

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
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  letterSpacing: vars.typescale.bodySmall.tracking
})

export const flag = style({
  flex: 'none',
  maxWidth: '100%',
  padding: '4px 8px',
  borderRadius: vars.shape.cornerFull,
  background: vars.state.warning,
  color: vars.state.warningContent,
  fontSize: vars.typescale.labelMedium.size,
  fontWeight: vars.typescale.labelMedium.weight,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  letterSpacing: vars.typescale.labelMedium.tracking,
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
