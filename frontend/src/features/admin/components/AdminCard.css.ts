import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
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
  color: vars.color.text.primary,
  fontSize: vars.typography.titleLarge.size,
  fontWeight: vars.typography.titleLarge.weight,
  letterSpacing: vars.typography.titleLarge.tracking,
  lineHeight: vars.typography.titleLarge.lineHeight
})

export const icon = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  inlineSize: '40px',
  blockSize: '40px',
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  color: vars.color.accent.solid
})

export const meta = style({
  flex: '1 1 auto',
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const subtitle = style({
  margin: '4px 0 0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})
