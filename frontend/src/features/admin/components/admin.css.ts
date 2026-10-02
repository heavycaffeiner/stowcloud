import { style } from '@vanilla-extract/css'
import { fadeInUp, vars } from '@/shared/theme'

export const section = style({
  animation: `${fadeInUp} 220ms cubic-bezier(0.2, 0, 0, 1)`,
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0',
  marginBlock: '0'
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '24px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexDirection: 'column',
      alignItems: 'stretch',
      gap: '12px'
    }
  }
})

export const sectionHint = style({
  margin: '0',
  fontWeight: vars.typography.bodySmall.weight,
  letterSpacing: vars.typography.bodySmall.tracking,
  maxWidth: '40rem',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const sectionFieldHint = style({
  margin: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  letterSpacing: vars.typography.bodySmall.tracking,
  lineHeight: vars.typography.bodySmall.lineHeight
})

export const sectionError = style({
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  margin: '8px 0 0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: '0'
})

export const item = style({
  minWidth: '0',
  selectors: {
    '& + &': {
      borderTop: `1px solid ${vars.color.border.subtle}`
    }
  }
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px',
  minWidth: '0'
})

export const rowName = style({
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const rowActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: '8px',
  flex: '0 0 auto',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      justifyContent: 'flex-start'
    }
  }
})

export const chip = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  minBlockSize: '24px',
  padding: '2px 9px',
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  fontSize: vars.typography.caption.size,
  fontWeight: vars.typography.caption.weight,
  lineHeight: vars.typography.caption.lineHeight,
  whiteSpace: 'nowrap'
})

// A chip that ends in its own remove button, which sets the height.
export const removableChip = style({
  gap: vars.space.xxs,
  paddingBlock: 0,
  paddingInlineStart: vars.space.md,
  paddingInlineEnd: 0
})

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  width: 'min(36rem, 100%)',
  minWidth: '0'
})

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '8px',
  padding: '32px 16px',
  color: vars.color.text.secondary,
  textAlign: 'center',
  border: `1px dashed ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent'
})

export const emptyText = style({
  margin: '0'
})

export const sectionHeaderAction = style({
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start'
    }
  }
})

export const sectionTitle = style({
  margin: '0',
  color: vars.color.text.primary,
  fontSize: vars.typography.titleLarge.size,
  fontWeight: vars.typography.titleLarge.weight,
  letterSpacing: vars.typography.titleLarge.tracking,
  lineHeight: vars.typography.titleLarge.lineHeight
})

export const hint = style({
  maxWidth: '40rem',
  margin: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const note = style({
  maxWidth: '40rem',
  margin: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const error = style({
  margin: '0',
  color: vars.color.danger.solid,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const warning = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.xs,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft,
  overflowWrap: 'anywhere'
})

export const storageToggleRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'flex-start',
      flexDirection: 'column'
    }
  }
})
