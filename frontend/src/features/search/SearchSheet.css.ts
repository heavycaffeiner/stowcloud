import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  inset: '48px 0 auto',
  width: 'min(720px, calc(100% - 32px))',
  maxHeight: 'calc(100dvh - 80px)',
  margin: '0 auto',
  padding: '0',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: '16px',
  background: vars.surface.overlay,
  color: vars.content.primary,
  boxShadow: '0 16px 48px rgba(0, 0, 0, 0.32)',
  overflow: 'visible',
  selectors: {
    '&::backdrop': {
      background: `rgba(${vars.color.scrim}, 0.5)`,
      backdropFilter: 'blur(6px)'
    }
  },
  '@media': {
    '(max-height: 400px)': {
      inset: '8px 0 auto',
      maxHeight: 'calc(100dvh - 16px)'
    }
  }
})

export const body = style({
  maxHeight: 'calc(100dvh - 80px)',
  display: 'flex',
  flexDirection: 'column',
  padding: '16px',
  boxSizing: 'border-box',
  overflow: 'visible',
  '@media': {
    '(max-height: 400px)': {
      maxHeight: 'calc(100dvh - 16px)'
    }
  }
})
