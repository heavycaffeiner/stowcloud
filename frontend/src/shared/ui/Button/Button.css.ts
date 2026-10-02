import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

// Mantine's disabled fill comes from the theme's disabled colors; only the text variant stays bare.
export const root = style({
  vars: {
    '--button-height': vars.density.control,
    '--button-radius': vars.radius.full,
    '--button-padding-x': vars.space.xl,
    '--button-fz': vars.typography.label.size
  },
  fontWeight: vars.typography.label.weight,
  letterSpacing: vars.typography.label.tracking,
  selectors: {
    '&[data-variant="text"]:where(:disabled, [data-disabled])': { background: 'transparent' }
  }
})
