import { style, styleVariants } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const content = style({
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  boxShadow: vars.elevation.lg
})

const side = { flex: '0 0 auto', inlineSize: 'min(20rem, 85vw)' }
const edge = { blockSize: 'auto', maxBlockSize: '85dvh' }

// The rounded corners face away from the edge the drawer is attached to.
export const position = styleVariants({
  left: { ...side, borderStartEndRadius: vars.radius.lg, borderEndEndRadius: vars.radius.lg },
  right: { ...side, borderStartStartRadius: vars.radius.lg, borderEndStartRadius: vars.radius.lg },
  top: {
    ...edge,
    borderEndStartRadius: vars.radius.lg,
    borderEndEndRadius: vars.radius.lg,
    paddingBlockStart: 'env(safe-area-inset-top, 0px)'
  },
  bottom: {
    ...edge,
    borderStartStartRadius: vars.radius.lg,
    borderStartEndRadius: vars.radius.lg,
    paddingBlockEnd: 'env(safe-area-inset-bottom, 0px)'
  }
})
