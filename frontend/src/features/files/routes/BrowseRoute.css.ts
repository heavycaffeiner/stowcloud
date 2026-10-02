import { style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  minHeight: '0',
  position: 'relative',
  background: vars.color.surface.page,
  selectors: {
    [`${appShellStyles.compact} &`]: {
      flex: '1',
      height: 'auto'
    }
  }
})
