import { style } from '@vanilla-extract/css'
import { figures } from './admin.css'
import { media, typography, vars } from '@/shared/theme'

export const adminSaved = style({
  margin: 0,
  color: vars.color.accent.solid,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const estimate = style([
  figures,
  {
    padding: vars.space.lg,
    border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
    borderRadius: vars.radius.md,
    background: 'transparent'
  }
])

// Top-aligned so a field that shows an error does not shift the one beside it.
export const form = style({
  display: 'flex',
  alignItems: 'flex-start',
  flexWrap: 'wrap',
  gap: vars.space.lg,
  maxWidth: vars.layout.form,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const field = style({
  flex: '1 1 180px',
  '@media': {
    [media.compact]: {
      flex: 'none'
    }
  }
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  flexShrink: 0,
  '@media': {
    [media.compact]: {
      flexWrap: 'wrap'
    }
  }
})
