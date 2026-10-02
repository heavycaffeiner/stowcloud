import { style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { compactFloatBottom, media, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  minHeight: 0,
  background: vars.color.surface.container,
  color: vars.color.text.primary
})

export const toolbar = style({
  zIndex: 2,
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  padding: `${vars.space.md} ${vars.space.xl}`,
  background: vars.color.surface.page,
  boxShadow: `0 ${vars.stroke.thin} 0 ${vars.color.border.subtle}`,
  '@media': {
    [media.compact]: {
      gap: vars.space.sm,
      padding: vars.space.sm
    }
  }
})

export const fileIcon = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.density.controlDesktop,
  height: vars.density.controlDesktop,
  borderRadius: vars.radius.md,
  background: vars.color.accent.soft,
  color: vars.color.accent.onSoft,
  '@media': {
    [media.compact]: {
      display: 'none'
    }
  }
})

export const identity = style({
  display: 'flex',
  flex: 1,
  flexDirection: 'column',
  gap: vars.space.xs,
  minWidth: 0
})

export const title = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      gap: vars.space.xs
    }
  }
})

export const details = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const filename = style({
  overflow: 'hidden',
  color: vars.color.text.primary,
  ...typography('title'),
  fontWeight: vars.font.weight.bold,
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

// On a phone the unsaved badge shrinks to a dot; its text stays for screen readers and the hover title.
export const dirty = style({
  '@media': {
    [media.compact]: {
      selectors: {
        [`${title} &`]: {
          width: vars.space.sm,
          minHeight: vars.space.sm,
          padding: 0,
          overflow: 'hidden',
          color: 'transparent',
          fontSize: 0
        }
      }
    }
  }
})

export const meta = style({
  flex: 'none',
  '@media': {
    [media.compact]: {
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
  flex: 1,
  minHeight: 0,
  padding: `${vars.space.lg} ${vars.space.xl} ${vars.space.xl}`,
  '@media': {
    [media.compact]: {
      padding: 0
    }
  }
})

export const loading = style({
  display: 'flex',
  flex: 1,
  alignItems: 'center',
  justifyContent: 'center'
})

export const locked = style({
  display: 'flex',
  flex: 1,
  alignItems: 'center',
  justifyContent: 'center',
  flexDirection: 'column',
  gap: vars.space.lg,
  padding: vars.space.xl,
  textAlign: 'center'
})

export const error = style({
  margin: 0,
  padding: vars.space.xl,
  color: vars.color.danger.solid
})

// The snackbar sits above the compact layout's navigation bar.
export const snackbar = style({
  selectors: { [`${appShellStyles.compact} &`]: { insetBlockEnd: compactFloatBottom } }
})
