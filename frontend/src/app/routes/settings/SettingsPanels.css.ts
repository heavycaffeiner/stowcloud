import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const pageGrid = style({
  display: 'grid',
  gap: '0',
  minWidth: '0'
})

export const cardIcon = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  borderRadius: vars.shape.cornerSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.primary})`
})

export const avatar = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.primary})`,
  color: `rgb(${vars.color.onPrimary})`,
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: '600'
})

export const accountName = style({
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.bodyLarge.size,
  fontWeight: '500'
})

export const username = style({
  margin: '0',
  overflowWrap: 'anywhere',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const cardHint = style({
  overflowWrap: 'anywhere',
  maxWidth: '30rem',
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px',
  minWidth: '0'
})

export const rowSegmented = style({
  display: 'block',
  maxWidth: '100%',
  overflowX: 'auto',
  paddingBlock: '2px',
  scrollbarWidth: 'none',
  selectors: {
    '&::-webkit-scrollbar': {
      display: 'none'
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      maxWidth: `calc(100vw - (2 * ${vars.layout.contentPad}))`
    }
  }
})

export const segmented = style({
  width: 'max-content',
  minWidth: 'max-content',
  maxWidth: '100%'
})

export const error = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})
