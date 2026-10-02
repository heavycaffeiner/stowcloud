import { keyframes, style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../../app/shell/AppShell.css'
import { compactFloatBottom, vars } from '@/shared/theme'

const barEnter = keyframes({
  from: { opacity: '0', transform: 'translate(-50%, 16px) scale(0.96)' },
  to: { opacity: '1', transform: 'translate(-50%, 0) scale(1)' }
})

export const bar = style({
  position: 'fixed',
  left: '50%',
  transform: 'translateX(-50%)',
  bottom: 'calc(24px + env(safe-area-inset-bottom, 0px))',
  zIndex: '25',
  maxWidth: 'calc(100vw - 32px)',
  borderRadius: '28px',
  background: vars.color.surface.overlay,
  border: `1px solid ${vars.color.border.subtle}`,
  boxShadow: '0 8px 32px rgba(0, 0, 0, 0.32)',
  animation: `${barEnter} 180ms cubic-bezier(0.2, 0, 0, 1)`,
  selectors: {
    [`${appShellStyles.compact} &`]: {
      insetBlockEnd: compactFloatBottom
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
  fontSize: vars.typography.label.size,
  lineHeight: vars.typography.label.lineHeight,
  fontWeight: '500',
  color: vars.color.text.primary
})

export const divider = style({
  width: '1px',
  height: '18px',
  background: vars.color.border.subtle,
  margin: '0 2px',
  flex: 'none'
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px'
})
