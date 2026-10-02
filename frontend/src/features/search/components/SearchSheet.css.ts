import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

const gap = vars.space.xxl
const shortGap = vars.space.sm

// A floating sheet centered near the top, rather than a panel across the whole edge.
export const root = style({
  flex: `0 1 min(45rem, calc(100% - 2 * ${gap}))`,
  maxBlockSize: `calc(100dvh - 2 * ${gap})`,
  margin: `${gap} auto 0`,
  display: 'flex',
  flexDirection: 'column',
  padding: vars.space.lg,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.lg,
  background: vars.color.surface.overlay,
  boxShadow: vars.elevation.xl,
  '@media': {
    '(max-height: 400px)': {
      maxBlockSize: `calc(100dvh - 2 * ${shortGap})`,
      marginBlockStart: shortGap
    }
  }
})
