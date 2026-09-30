import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const focusRing = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const touchTarget = style({
  position: 'relative',
  selectors: {
    '&::before': {
      content: "''",
      position: 'absolute',
      inset: '50% auto auto 50%',
      width: `max(100%, ${vars.control.minCompact})`,
      height: `max(100%, ${vars.control.minCompact})`,
      translate: '-50% -50%'
    }
  }
})

export const srOnly = style({
  position: 'absolute',
  width: '1px',
  height: '1px',
  padding: '0',
  margin: '-1px',
  overflow: 'hidden',
  clipPath: 'inset(50%)',
  whiteSpace: 'nowrap',
  border: '0'
})
