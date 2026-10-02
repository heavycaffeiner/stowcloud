import { createVar, fallbackVar } from '@vanilla-extract/css'
import { vars } from './contract.css'

// The app shell writes the upload tray's top edge here at runtime so floating bars can stay above it.
export const trayStackTop = createVar()

/** The bottom offset of a floating bar on the compact layout: above the navigation bar and the trays. */
export const compactFloatBottom = `max(calc(${vars.space.lg} + ${vars.layout.navBar} + env(safe-area-inset-bottom, 0px)), ${fallbackVar(trayStackTop, '0px')})`
