import { style } from '@vanilla-extract/css'
import { vars } from './contract.css'

/** The focus outline itself, for a part that shows focus held by another element. */
export const focusOutline = {
  outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
  outlineOffset: vars.focusRing.offset
}

/** The one keyboard focus indicator. Mantine applies it to its focusable parts through the theme. */
export const focusRing = style({
  selectors: { '&:focus-visible': focusOutline }
})

/** Press feedback for buttons and other activatable surfaces. */
export const pressable = style({
  transition: `transform ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:active:not(:disabled, [data-disabled], [data-loading])': { transform: 'scale(0.97)' }
  }
})

/** Grows the hit area to the touch minimum without changing the layout box. */
export const touchTarget = style({
  position: 'relative',
  selectors: {
    '&::before': {
      content: "''",
      position: 'absolute',
      inset: '50% auto auto 50%',
      width: `max(100%, ${vars.density.controlCompact})`,
      height: `max(100%, ${vars.density.controlCompact})`,
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
