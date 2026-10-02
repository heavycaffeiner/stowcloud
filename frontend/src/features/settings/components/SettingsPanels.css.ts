import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

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
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  color: vars.color.accent.solid
})

export const avatar = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  borderRadius: vars.radius.full,
  background: vars.color.accent.solid,
  color: vars.color.accent.onSolid,
  fontSize: vars.typography.title.size,
  fontWeight: '600'
})

export const accountName = style({
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typography.bodyLarge.size,
  fontWeight: '500'
})

export const username = style({
  margin: '0',
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size
})

export const cardHint = style({
  overflowWrap: 'anywhere',
  maxWidth: '30rem',
  margin: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  letterSpacing: vars.typography.bodySmall.tracking,
  lineHeight: vars.typography.bodySmall.lineHeight
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
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})
