import { fallbackVar, keyframes, style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { trayStackTop, vars } from '../../../ui/theme.css'

const barEnter = keyframes({
  from: { opacity: '0', transform: 'translate(-50%, 16px) scale(0.96)' },
  to: { opacity: '1', transform: 'translate(-50%, 0) scale(1)' }
})

export const iconBtn = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 10%, transparent)`,
      color: vars.content.primary
    }
  },
  width: vars.control.min,
  height: vars.control.min,
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.content.secondary,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  cursor: 'pointer',
  padding: '0',
  transition: 'background-color 120ms ease, color 120ms ease'
})

export const bar = style({
  position: 'fixed',
  left: '50%',
  transform: 'translateX(-50%)',
  bottom: 'calc(24px + env(safe-area-inset-bottom, 0px))',
  zIndex: '25',
  maxWidth: 'calc(100vw - 32px)',
  borderRadius: '28px',
  background: vars.surface.overlay,
  border: `1px solid ${vars.outline.variant}`,
  boxShadow: '0 8px 32px rgba(0, 0, 0, 0.32)',
  animation: `${barEnter} 180ms cubic-bezier(0.2, 0, 0, 1)`,
  selectors: {
    [`${appShellStyles.compact} &`]: {
      bottom: `max(calc(16px + ${vars.layout.navBarHeight} + env(safe-area-inset-bottom, 0px)), ${fallbackVar(trayStackTop, '0px')})`
    }
  }
})

export const barInner = style({
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  height: '48px',
  padding: '0 14px 0 10px',
  whiteSpace: 'nowrap'
})

export const count = style({
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '500',
  color: vars.content.primary
})

export const divider = style({
  width: '1px',
  height: '18px',
  background: vars.outline.variant,
  margin: '0 2px',
  flex: 'none'
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px'
})
