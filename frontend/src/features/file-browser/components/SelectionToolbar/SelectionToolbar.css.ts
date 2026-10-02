import { keyframes, style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../../app/shell/AppShell.css'
import { compactFloatBottom, typography, vars } from '@/shared/theme'

const barEnter = keyframes({
  from: { opacity: 0, transform: `translate(-50%, ${vars.space.lg}) scale(0.96)` },
  to: { opacity: 1, transform: 'translate(-50%, 0) scale(1)' }
})

export const bar = style({
  position: 'fixed',
  left: '50%',
  transform: 'translateX(-50%)',
  bottom: `calc(${vars.space.xl} + env(safe-area-inset-bottom, 0px))`,
  zIndex: 25,
  maxWidth: `calc(100vw - 2 * ${vars.space.lg})`,
  borderRadius: vars.radius.full,
  background: vars.color.surface.overlay,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  boxShadow: vars.elevation.xl,
  animation: `${barEnter} ${vars.motion.medium} ${vars.motion.easing}`,
  selectors: {
    [`${appShellStyles.compact} &`]: {
      insetBlockEnd: compactFloatBottom
    }
  }
})

export const barInner = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  height: vars.density.row,
  padding: `0 ${vars.space.md} 0 ${vars.space.sm}`,
  whiteSpace: 'nowrap'
})

export const count = style({
  ...typography('label'),
  color: vars.color.text.primary
})

export const divider = style({
  width: vars.stroke.thin,
  height: vars.space.lg,
  background: vars.color.border.subtle,
  margin: `0 ${vars.space.xxs}`,
  flex: 'none'
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.xxs
})
