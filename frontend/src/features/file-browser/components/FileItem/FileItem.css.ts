import { globalStyle, keyframes, style } from '@vanilla-extract/css'
import { focusOutline, media, typography, vars } from '@/shared/theme'
import { container, root as viewRoot } from '../file-view.css'

/** Marks a list laid out for a phone: two-line rows with only the check and the menu beside the name. */
export const mobile = style({})

const narrow = `${container} ${media.compact}`

const pulse = keyframes({
  '0%, 100%': { opacity: 0.5 },
  '50%': { opacity: 1 }
})

const lift = `${vars.motion.short} ${vars.motion.easing}`

export const icon = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  color: vars.color.text.icon
})

export const check = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  inlineSize: vars.density.control,
  blockSize: vars.density.control
})

// The item's own text color, so the button follows it into the selected state.
export const kebab = style({
  flex: 'none',
  color: 'inherit'
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  paddingInline: vars.layout.contentPad,
  color: vars.color.text.primary,
  ...typography('bodyLarge'),
  cursor: 'pointer',
  userSelect: 'none',
  WebkitUserSelect: 'none',
  transition: `background-color ${lift}, color ${lift}`,
  selectors: {
    '&:hover': { background: `color-mix(in srgb, ${vars.color.text.primary} 5%, transparent)` },
    [`${mobile} &`]: {
      display: 'grid',
      gridTemplateColumns: `${vars.density.controlCompact} minmax(0, 1fr) ${vars.density.controlCompact}`,
      paddingInline: vars.space.md
    }
  },
  '@container': { [narrow]: { paddingInline: vars.space.lg } }
})

export const card = style({
  position: 'relative',
  display: 'flex',
  flexDirection: 'column',
  minInlineSize: 0,
  boxSizing: 'border-box',
  overflow: 'hidden',
  borderRadius: vars.radius.md,
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  boxShadow: vars.elevation.sm,
  cursor: 'pointer',
  userSelect: 'none',
  WebkitUserSelect: 'none',
  transition: `transform ${lift}, background-color ${lift}, box-shadow ${lift}`,
  selectors: {
    '&:hover': {
      transform: `translateY(calc(-1 * ${vars.stroke.thin}))`,
      background: vars.color.surface.raised,
      boxShadow: vars.elevation.md
    },
    '&:active': { transform: 'scale(0.98)' }
  }
})

// A folder card is a single line: check, icon, name and menu.
export const cardFolder = style({
  flexDirection: 'row',
  alignItems: 'center',
  gap: vars.space.sm,
  paddingInline: vars.space.sm
})

// A selected row darkens on hover; a selected card keeps its color and only lifts.
export const selected = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  selectors: {
    '&:hover': { background: `color-mix(in srgb, ${vars.color.selection.bg} 85%, ${vars.color.text.primary})` },
    [`${card}&:hover`]: { background: vars.color.selection.bg }
  }
})

// The keyboard's position, shown while the view itself holds focus.
export const focused = style({
  selectors: {
    [`${viewRoot}:focus-visible &`]: { ...focusOutline, outlineOffset: vars.focusRing.insetOffset }
  }
})

/** A list column. The header, the rows and their placeholders share these so the columns line up. */
export const cell = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minInlineSize: 0,
  overflow: 'hidden'
})

export const colSelect = style({
  flex: `0 0 ${vars.density.controlDesktop}`,
  justifyContent: 'center',
  overflow: 'visible',
  selectors: { [`${mobile} &`]: { flexBasis: vars.density.controlCompact, blockSize: vars.density.controlCompact } }
})

export const colName = style({
  flex: '1 1 auto'
})

export const colSize = style({
  flex: '0 0 7rem',
  justifyContent: 'flex-end',
  '@container': { [narrow]: { flexBasis: '5.5rem' } },
  selectors: { [`${mobile} &`]: { display: 'none' } }
})

export const colModified = style({
  flex: '0 0 11rem',
  justifyContent: 'flex-end',
  whiteSpace: 'nowrap',
  '@container': { [narrow]: { display: 'none' } },
  selectors: { [`${mobile} &`]: { display: 'none' } }
})

export const colActions = style({
  flex: `0 0 ${vars.density.controlDesktop}`,
  justifyContent: 'center',
  overflow: 'visible',
  selectors: { [`${mobile} &`]: { flexBasis: vars.density.controlCompact, blockSize: vars.density.controlCompact } }
})

export const detail = style({
  color: vars.color.text.secondary,
  ...typography('body'),
  fontVariantNumeric: 'tabular-nums',
  selectors: { [`${selected} &`]: { color: 'inherit' } }
})

export const nameCopy = style({
  display: 'flex',
  flex: '1 1 auto',
  minInlineSize: 0,
  selectors: {
    [`${mobile} &`]: { flexDirection: 'column', justifyContent: 'center', gap: vars.space.xxs }
  }
})

export const rowName = style({
  flex: '1 1 auto',
  overflow: 'hidden',
  selectors: { [`${mobile} &`]: { inlineSize: '100%' } }
})

export const mobileMeta = style({
  display: 'none',
  selectors: {
    [`${mobile} &`]: {
      display: 'block',
      maxInlineSize: '100%',
      overflow: 'hidden',
      color: vars.color.text.secondary,
      ...typography('bodySmall'),
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap'
    },
    [`${mobile} ${selected} &`]: { color: 'inherit' }
  }
})

// A list row's menu button appears with hover, focus or selection, and stays on wherever there is no hover.
export const rowKebab = style({
  opacity: 0,
  transition: `opacity ${lift}`,
  selectors: {
    [`${row}:hover &, ${selected} &, ${focused} &, &:focus-visible, ${mobile} &`]: { opacity: 1 }
  },
  '@media': { [media.touch]: { opacity: 1 } }
})

export const cardHead = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minBlockSize: vars.density.controlDesktop,
  paddingBlock: vars.space.xs,
  paddingInline: vars.space.sm
})

export const cardName = style({
  flex: 1,
  overflow: 'hidden',
  ...typography('body')
})

export const thumb = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  minBlockSize: 0,
  marginInline: vars.space.sm,
  overflow: 'hidden',
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary
})

globalStyle(`${thumb} img`, {
  transition: `transform ${vars.motion.medium} ${vars.motion.easing}`
})

globalStyle(`${card}:hover ${thumb} img`, {
  transform: 'scale(1.04)'
})

export const cardMeta = style({
  paddingBlock: `${vars.space.xs} ${vars.space.sm}`,
  paddingInline: vars.space.sm,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  selectors: { [`${selected} &`]: { color: 'inherit' } }
})

// Placeholders hold the place of items still loading. They take no pointer feedback.
export const placeholderRow = style({
  cursor: 'default',
  selectors: { '&:hover': { background: 'transparent' } }
})

export const placeholderCard = style({
  gap: vars.space.sm,
  padding: vars.space.sm,
  cursor: 'default',
  selectors: {
    '&:hover, &:active': { transform: 'none', background: vars.color.surface.container, boxShadow: vars.elevation.sm }
  }
})

export const bar = style({
  display: 'block',
  blockSize: vars.space.lg,
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  animation: `${pulse} 1.2s ease-in-out infinite`,
  '@media': { [media.reducedMotion]: { animation: 'none' } }
})

export const barIcon = style({
  flex: 'none',
  inlineSize: vars.space.xl,
  blockSize: vars.space.xl,
  borderRadius: vars.radius.sm
})

export const barShort = style({
  inlineSize: '40%'
})

export const barLong = style({
  inlineSize: '60%'
})

export const barBlock = style({
  flex: 1,
  borderRadius: vars.radius.sm
})
