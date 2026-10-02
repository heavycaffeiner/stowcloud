import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  padding: '24px 0',
  minWidth: '0',
  border: '0',
  borderBottom: `1px solid ${vars.color.border.subtle}`,
  borderRadius: '0',
  background: 'transparent',
  color: vars.color.text.primary,
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
  color: vars.color.text.primary,
  fontSize: vars.typography.titleLarge.size,
  fontWeight: vars.typography.titleLarge.weight,
  letterSpacing: vars.typography.titleLarge.tracking,
  lineHeight: vars.typography.titleLarge.lineHeight,
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
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  fontSize: vars.typography.caption.size,
  fontWeight: vars.typography.caption.weight,
  lineHeight: vars.typography.caption.lineHeight,
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
