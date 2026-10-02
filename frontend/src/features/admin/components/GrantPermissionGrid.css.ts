import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '0',
  maxWidth: '100%',
  overflow: 'hidden',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.sm,
  background: 'transparent'
})

export const head = style({
  display: 'grid',
  gridTemplateColumns: '1fr 72px 72px',
  alignItems: 'center',
  gap: '8px',
  padding: '8px 12px',
  color: vars.color.text.secondary,
  background: vars.color.surface.container,
  fontSize: vars.typography.bodySmall.size,
  '@media': {
    '(max-width: 599.98px)': {
      gridTemplateColumns: 'minmax(0, 1fr) 52px 52px',
      paddingInline: '8px'
    }
  }
})

export const row = style({
  display: 'grid',
  gridTemplateColumns: '1fr 72px 72px',
  alignItems: 'center',
  gap: '8px',
  padding: '8px 12px',
  background: 'transparent',
  selectors: {
    '& + &': {
      borderTop: `1px solid ${vars.color.border.subtle}`
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      gridTemplateColumns: 'minmax(0, 1fr) 52px 52px',
      paddingInline: '8px'
    }
  }
})

export const cell = style({
  display: 'flex',
  justifyContent: 'center'
})
