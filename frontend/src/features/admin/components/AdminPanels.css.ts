import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const pageSection = style({
  minWidth: '0'
})

export const pageTitle = style({
  margin: '0 0 8px',
  color: vars.color.text.primary,
  fontSize: vars.typography.titleLarge.size,
  fontWeight: vars.typography.titleLarge.weight,
  letterSpacing: vars.typography.titleLarge.tracking,
  lineHeight: vars.typography.titleLarge.lineHeight
})

export const loading = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  color: vars.color.text.secondary
})
