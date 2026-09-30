import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const selectLabel = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '6px',
  fontSize: vars.typescale.bodyMedium.size,
  color: `rgb(${vars.color.onSurface})`,
  width: '100%'
})

export const select = style({
  minBlockSize: '48px',
  width: '100%',
  maxWidth: '24rem',
  padding: '10px 16px',
  border: 'none',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.bodyLarge.size,
  cursor: 'pointer',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})
