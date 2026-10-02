import { fallbackVar, style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { trayStackTop, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  minHeight: '0',
  background: vars.color.surface.container,
  color: vars.color.text.primary
})

export const toolbar = style({
  zIndex: '2',
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  minHeight: '76px',
  padding: '10px 20px',
  background: vars.color.surface.page,
  boxShadow: `0 1px 0 ${vars.color.border.subtle}`,
  '@media': {
    '(max-width: 600px)': {
      gap: '8px',
      minHeight: '64px',
      padding: '8px'
    }
  }
})

export const fileIcon = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  borderRadius: vars.radius.md,
  background: vars.color.accent.soft,
  color: vars.color.accent.onSoft,
  fontSize: '1.35rem',
  '@media': {
    '(max-width: 600px)': {
      display: 'none'
    }
  }
})

export const identity = style({
  display: 'flex',
  flex: '1',
  flexDirection: 'column',
  gap: '4px',
  minWidth: '0'
})

export const title = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  '@media': {
    '(max-width: 600px)': {
      gap: '6px'
    }
  }
})

export const details = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: '1.25rem'
})

export const filename = style({
  overflow: 'hidden',
  color: vars.color.text.primary,
  fontSize: vars.typography.title.size,
  fontWeight: '600',
  lineHeight: '1.35',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const language = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  minHeight: '22px',
  paddingInline: '8px',
  borderRadius: vars.radius.full,
  fontSize: vars.typography.caption.size,
  fontWeight: '600',
  lineHeight: '1',
  background: vars.color.selection.bg,
  color: vars.color.selection.fg
})

export const badge = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  minHeight: '22px',
  paddingInline: '8px',
  borderRadius: vars.radius.full,
  fontSize: vars.typography.caption.size,
  fontWeight: '600',
  lineHeight: '1'
})

export const badgeDirty = style({
  background: vars.color.accent.soft,
  color: vars.color.accent.onSoft,
  '@media': {
    '(max-width: 600px)': {
      width: '8px',
      minHeight: '8px',
      padding: '0',
      overflow: 'hidden',
      color: 'transparent',
      fontSize: '0'
    }
  }
})

export const badgeReadonly = style({
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const meta = style({
  flex: 'none',
  '@media': {
    '(max-width: 600px)': {
      display: 'none'
    }
  }
})

export const actions = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center'
})

export const body = style({
  position: 'relative',
  display: 'flex',
  flex: '1',
  minHeight: '0',
  padding: '16px 20px 20px',
  '@media': {
    '(max-width: 600px)': {
      padding: '0'
    }
  }
})

export const loading = style({
  display: 'flex',
  flex: '1',
  alignItems: 'center',
  justifyContent: 'center'
})

export const locked = style({
  display: 'flex',
  flex: '1',
  alignItems: 'center',
  justifyContent: 'center',
  flexDirection: 'column',
  gap: '16px',
  padding: '24px',
  textAlign: 'center'
})

export const error = style({
  margin: '0',
  padding: '24px',
  color: vars.color.danger.solid
})

// mdui sets the snackbar's bottom inline, so lifting it above the compact nav bar needs !important.
export const snackbar = style({
  selectors: {
    [`${appShellStyles.compact} &`]: {
      bottom: `max(calc(16px + ${vars.layout.navBar} + env(safe-area-inset-bottom, 0px)), ${fallbackVar(trayStackTop, '0px')}) !important`
    }
  }
})
