import { style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

export const categoryPill = style({
  flex: 'none',
  '@media': { [media.compact]: { scrollSnapAlign: 'start' } }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  width: '100%',
  color: vars.color.text.primary
})

export const queryBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  height: '48px',
  padding: '0 12px 0 14px',
  background: vars.color.surface.raised,
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: '12px',
  boxSizing: 'border-box',
  transition: 'border-color 150ms ease, box-shadow 150ms ease',
  selectors: {
    '&:focus-within': {
      borderColor: vars.color.accent.solid,
      boxShadow: `0 0 0 2px color-mix(in srgb, ${vars.color.accent.solid} 20%, transparent)`
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      height: '56px',
      paddingBlock: '5px'
    }
  }
})

export const queryIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  color: vars.color.text.secondary,
  flex: 'none'
})

export const input = style({
  flex: '1',
  minWidth: '0',
  height: '100%',
  border: 'none',
  background: 'transparent',
  color: vars.color.text.primary,
  fontFamily: 'inherit',
  fontSize: vars.typography.bodyLarge.size,
  lineHeight: vars.typography.bodyLarge.lineHeight,
  outline: 'none',
  padding: '0',
  selectors: {
    '&::placeholder': {
      color: vars.color.text.secondary
    }
  }
})

export const filterBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '8px',
  padding: '2px 0 4px',
  overflow: 'visible',
  scrollbarWidth: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column',
      gap: '8px',
      overflow: 'visible'
    }
  }
})

export const categories = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  flex: '1 1 auto',
  overflowX: 'auto',
  scrollbarWidth: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      flex: 'none',
      paddingBottom: '2px',
      overscrollBehaviorInline: 'contain',
      scrollSnapType: 'x proximity'
    }
  }
})

export const scopePill = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '5px',
  height: '30px',
  padding: '0 10px',
  borderRadius: '15px',
  background: `color-mix(in srgb, ${vars.color.selection.bg} 55%, transparent)`,
  border: `1px solid color-mix(in srgb, ${vars.color.selection.fg} 25%, transparent)`,
  color: vars.color.selection.fg,
  fontSize: vars.typography.labelSmall.size,
  lineHeight: vars.typography.labelSmall.lineHeight,
  fontWeight: '500',
  whiteSpace: 'nowrap',
  flex: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start',
      minHeight: '32px'
    }
  }
})

export const statusBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  minHeight: '24px',
  padding: '0 2px',
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  color: vars.color.text.secondary,
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column',
      gap: '8px'
    }
  }
})

export const statusInfo = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '6px'
})

export const progress = style({
  display: 'inline-flex',
  alignItems: 'center',
  width: '14px',
  height: '14px'
})

export const statusActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  '@media': {
    '(max-width: 599.98px)': {
      justifyContent: 'flex-end'
    }
  }
})
