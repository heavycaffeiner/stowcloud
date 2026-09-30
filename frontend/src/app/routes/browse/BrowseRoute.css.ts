import { style } from '@vanilla-extract/css'
import * as appShellStyles from '../../AppShell.css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  minHeight: '0',
  position: 'relative',
  background: vars.surface.page,
  selectors: {
    [`${appShellStyles.compact} &`]: {
      flex: '1',
      height: 'auto'
    }
  }
})
