import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const pathRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const pathRowField = style({
  flex: '1 1 auto',
  minWidth: '0'
})

export const encAnnounce = style({
  minHeight: '1.25em',
  margin: '0',
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const pathRowButton = style({
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start'
    }
  }
})
