import { style } from '@vanilla-extract/css'
import * as iconButtonStyles from './IconButton.css'

export const root = style({
  selectors: {
    [`${iconButtonStyles.button} > &`]: {
      display: 'inline-flex',
      alignItems: 'center',
      justifyContent: 'center',
      verticalAlign: 'middle'
    }
  }
})
