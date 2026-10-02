import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

export const body = style({
  alignItems: 'center',
  minBlockSize: vars.density.row
})

export const label = style({
  ...typography('bodyLarge'),
  color: vars.color.text.primary
})

export const input = style({})

// The off state keeps a 3:1 outline and thumb so the switch reads against the page.
export const track = style({
  vars: { '--switch-color': vars.color.accent.solid },
  selectors: {
    [`${input}:not(:checked, [data-disabled]) + &`]: {
      vars: { '--switch-bg': vars.color.surface.fill },
      boxShadow: `inset 0 0 0 ${vars.stroke.thick} ${vars.color.border.strong}`
    },
    [`${input}:focus-visible + &`]: focusOutline
  }
})

export const thumb = style({
  selectors: {
    [`${input}:not(:checked, [data-disabled]) + * > &`]: {
      vars: { '--switch-thumb-bg': vars.color.border.strong },
      borderColor: 'transparent'
    }
  }
})
