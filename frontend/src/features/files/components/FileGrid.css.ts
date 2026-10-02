import { createVar, fallbackVar, keyframes, style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const selectionBarSpace = createVar()

const pulse = keyframes({
  '0%, 100%': {
    opacity: '.5'
  },
  '50%': {
    opacity: '1'
  }
})

export const root = style({
  position: 'relative',
  flex: '1',
  minWidth: '0',
  alignSelf: 'flex-start',
  contain: 'content',
  background: vars.color.surface.page,
  containerType: 'inline-size',
  paddingBottom: `calc(24px + ${fallbackVar(selectionBarSpace, '0px')})`,
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  }
})

export const contained = style({
  alignSelf: 'stretch',
  minHeight: '0',
  overflow: 'auto',
  overscrollBehaviorY: 'contain'
})

export const reserveSelection = style({
  vars: {
    [selectionBarSpace]: '80px'
  }
})

export const group = style({
  margin: '0',
  padding: `16px ${vars.layout.contentPad} 8px`,
  color: vars.color.text.secondary,
  fontSize: vars.typography.titleSmall.size,
  fontWeight: vars.typography.titleSmall.weight,
  lineHeight: vars.typography.titleSmall.lineHeight,
  letterSpacing: vars.typography.titleSmall.tracking
})

export const section = style({
  position: 'relative'
})

export const window = style({
  willChange: 'transform',
  paddingInline: vars.layout.contentPad
})

export const row = style({
  display: 'flex',
  alignItems: 'stretch',
  gap: '16px'
})

export const card = style({
  minWidth: '0',
  position: 'relative',
  boxSizing: 'border-box',
  height: '100%',
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  cursor: 'pointer',
  overflow: 'hidden',
  userSelect: 'none',
  WebkitUserSelect: 'none',
  boxShadow: vars.elevation.sm,
  transition:
    'transform 160ms cubic-bezier(0.2, 0, 0, 1), background-color 150ms ease, box-shadow 160ms cubic-bezier(0.2, 0, 0, 1)',
  selectors: {
    '&:hover': {
      transform: 'translateY(-1px)',
      boxShadow: '0 3px 10px rgba(0, 0, 0, 0.10)',
      background: vars.color.surface.raised
    },
    '&:active': {
      transform: 'scale(0.98)'
    }
  },
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
      transition: 'none'
    }
  }
})

export const cardSelected = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  border: 'none',
  boxShadow: vars.elevation.sm,
  selectors: {
    '&:hover': {
      background: vars.color.selection.bg
    }
  }
})

export const cardFolder = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  paddingInline: '8px',
  selectors: {
    [`${root}[data-density='compact'] &`]: {
      height: '44px'
    },
    [`${root}[data-density='comfortable'] &`]: {
      height: '52px'
    },
    [`${root}[data-density='spacious'] &`]: {
      height: '60px'
    }
  }
})

export const cardFile = style({
  display: 'flex',
  flexDirection: 'column',
  selectors: {
    [`${root}[data-density='compact'] &`]: {
      height: '176px'
    },
    [`${root}[data-density='comfortable'] &`]: {
      height: '208px'
    },
    [`${root}[data-density='spacious'] &`]: {
      height: '244px'
    }
  }
})

export const cardFocused = style({
  selectors: {
    [`${root}:focus-visible &`]: {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  }
})

export const head = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  padding: '4px 8px',
  minHeight: '40px'
})

export const type = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  color: vars.color.text.icon
})

export const name = style({
  flex: '1',
  overflow: 'hidden',
  fontSize: vars.typography.body.size,
  fontWeight: vars.typography.body.weight,
  lineHeight: vars.typography.body.lineHeight,
  letterSpacing: vars.typography.body.tracking
})

export const thumb = style({
  flex: '1',
  minHeight: '0',
  margin: '0 8px',
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  overflow: 'hidden'
})

export const thumbImg = style({
  width: '100%',
  height: '100%',
  objectFit: 'cover',
  transition: 'transform 220ms cubic-bezier(0.2, 0, 0, 1)',
  selectors: {
    [`${card}:hover &`]: {
      transform: 'scale(1.04)'
    }
  }
})

export const meta = style({
  padding: '4px 8px 8px',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  lineHeight: vars.typography.bodySmall.lineHeight,
  letterSpacing: vars.typography.bodySmall.tracking,
  selectors: {
    [`${cardSelected} &`]: {
      color: 'inherit'
    }
  }
})

export const check = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.density.control,
  height: vars.density.control
})

// The card's own text color, so the button follows the card into its selected state.
export const kebab = style({
  flex: 'none',
  color: 'inherit'
})

export const skeleton = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  padding: '8px',
  cursor: 'default'
})

export const skeletonLine = style({
  height: '12px',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  animation: `${pulse} 1.2s ease-in-out infinite`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
      transition: 'none'
    }
  }
})

export const skeletonBlock = style({
  flex: '1',
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  animation: `${pulse} 1.2s ease-in-out infinite`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      animation: 'none',
      transition: 'none'
    }
  }
})

export const empty = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  paddingBlock: '64px',
  color: vars.color.text.secondary
})
