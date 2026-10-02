import { createVar, fallbackVar, style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

/** Overrides the button's square size, which otherwise follows the control density. */
export const iconButtonSize = createVar()

export const root = style({
  vars: {
    '--ai-size': fallbackVar(iconButtonSize, vars.density.control),
    '--ai-radius': vars.radius.full
  },
  flex: 'none',
  selectors: {
    '&[data-variant="standard"]:where(:disabled, [data-disabled])': { background: 'transparent' }
  }
})
