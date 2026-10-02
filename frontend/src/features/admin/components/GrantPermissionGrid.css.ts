import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '0',
  maxWidth: '100%',
  overflow: 'hidden',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.small,
  background: 'transparent'
})

export const head = style({
  display: 'grid',
  gridTemplateColumns: '1fr 72px 72px',
  alignItems: 'center',
  gap: '8px',
  padding: '8px 12px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  background: `rgb(${vars.color.surfaceContainerLow})`,
  fontSize: vars.typescale.bodySmall.size,
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
      borderTop: `1px solid ${vars.outline.variant}`
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
