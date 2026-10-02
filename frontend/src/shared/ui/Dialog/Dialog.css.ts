import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

const safeInset = (side: 'top' | 'right' | 'bottom' | 'left') =>
  `max(${vars.space.lg}, env(safe-area-inset-${side}, 0px))`

export const overlay = style({
  background: `color-mix(in srgb, ${vars.color.scrim} 32%, transparent)`
})

export const inner = style({
  padding: vars.space.lg,
  '@media': { [media.compact]: { padding: vars.space.sm } }
})

export const content = style({
  flex: '0 1 auto',
  minInlineSize: 'min(17.5rem, 100%)',
  maxInlineSize: 'min(35rem, 100%)',
  maxBlockSize: '100%',
  background: vars.color.surface.overlay,
  color: vars.color.text.primary,
  borderRadius: vars.radius.lg,
  boxShadow: vars.elevation.lg
})

export const wide = style({ maxInlineSize: '100%' })

// Fills the viewport up to a cap and hands the whole panel to the body, for media that draws its own chrome.
export const viewerInner = style({
  padding: `${safeInset('top')} ${safeInset('right')} ${safeInset('bottom')} ${safeInset('left')}`,
  '@media': { [media.wide]: { padding: vars.space.xxl } }
})

export const viewer = style({
  display: 'flex',
  flexDirection: 'column',
  inlineSize: 'min(75rem, 100%)',
  maxInlineSize: '100%',
  blockSize: '100%',
  maxBlockSize: '56rem',
  overflow: 'hidden'
})

export const header = style({
  minBlockSize: 'auto',
  padding: `${vars.space.xl} ${vars.space.xl} ${vars.space.lg}`,
  background: 'inherit'
})

export const title = style({
  ...typography('heading'),
  color: vars.color.text.primary
})

export const body = style({
  ...typography('body'),
  color: vars.color.text.secondary,
  paddingBlock: 0,
  paddingInline: vars.space.xl,
  selectors: {
    '&:first-child': { paddingBlockStart: vars.space.xl },
    '&:last-child': { paddingBlockEnd: vars.space.xl },
    [`${viewer} &`]: { display: 'flex', flex: 1, minBlockSize: '0', padding: '0', overflow: 'hidden' }
  }
})

// Sticks to the bottom edge so a long form scrolls under the buttons rather than taking them with it.
export const actions = style({
  position: 'sticky',
  insetBlockEnd: 0,
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-end',
  gap: vars.space.sm,
  padding: vars.space.xl,
  background: 'inherit'
})
