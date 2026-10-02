// The sidebar's New button asks the browse page for its create menu.
import { signal } from '@preact/signals-react'

/** Where the create menu should open, in viewport coordinates. `align` says
 * which edge `x` refers to: a left-hand trigger anchors its left edge, a
 * right-hand one its right. */
export interface CreateMenuAnchor {
  readonly x: number
  readonly y: number
  readonly align: 'start' | 'end'
}

/** A request the browse page has not answered yet. */
export const createMenuRequest = signal<CreateMenuAnchor | null>(null)
