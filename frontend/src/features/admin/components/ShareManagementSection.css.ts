import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

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
  color: `rgb(${vars.color.primary})`,
  overflowWrap: 'anywhere'
})

export const pathRowButton = style({
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start'
    }
  }
})
