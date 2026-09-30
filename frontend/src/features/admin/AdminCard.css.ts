import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  padding: '24px 0',
  minWidth: '0',
  border: '0',
  borderBottom: `1px solid ${vars.outline.variant}`,
  borderRadius: '0',
  background: 'transparent',
  color: vars.content.primary,
  boxShadow: 'none',
  selectors: {
    '&:last-child': {
      borderBottom: '0'
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      padding: '16px 0',
      gap: '12px'
    }
  },
  scrollMarginTop: '24px',
  marginBottom: '0'
})

export const head = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '16px',
  minWidth: '0',
  marginBottom: '4px',
  '@media': {
    '(max-width: 599.98px)': {
      gap: '12px'
    }
  }
})

export const title = style({
  margin: '0 0 2px',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleLarge.size,
  fontWeight: vars.typescale.titleLarge.weight,
  letterSpacing: vars.typescale.titleLarge.tracking,
  lineHeight: vars.typescale.titleLarge.lineHeight
})

export const icon = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  inlineSize: '40px',
  blockSize: '40px',
  borderRadius: vars.shape.cornerSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.primary})`
})

export const meta = style({
  flex: '1 1 auto',
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const subtitle = style({
  margin: '4px 0 0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})
