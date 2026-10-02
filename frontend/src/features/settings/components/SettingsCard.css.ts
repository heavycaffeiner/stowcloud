import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const card = style({
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
  }
})

export const cardHead = style({
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

export const cardTitle = style({
  margin: '0 0 2px',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleLarge.size,
  fontWeight: vars.typescale.titleLarge.weight,
  letterSpacing: vars.typescale.titleLarge.tracking,
  lineHeight: vars.typescale.titleLarge.lineHeight,
  overflowWrap: 'anywhere'
})

export const cardMeta = style({
  flex: '1 1 auto',
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const text = style({
  overflowWrap: 'anywhere',
  margin: '0'
})

export const badge = style({
  display: 'inline-flex',
  alignItems: 'center',
  minBlockSize: '24px',
  marginInlineStart: '8px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelSmall.size,
  fontWeight: vars.typescale.labelSmall.weight,
  lineHeight: vars.typescale.labelSmall.lineHeight,
  whiteSpace: 'nowrap',
  verticalAlign: 'middle',
  selectors: {
    [`${cardHead} > &`]: {
      flex: 'none',
      marginInlineStart: 'auto'
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      selectors: {
        [`${cardHead} > &`]: {
          marginInlineStart: '0'
        }
      }
    }
  }
})
