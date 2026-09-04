// Selection model operations delegating to pure functional selection slice.
// Kept for compatibility while migrating components to Zustand stores.

export interface SelectionOps {
  /** Replace the whole selection with a single name. */
  selectOnly(selection: Set<string>, name: string): void
  /** Ctrl/Cmd-click: toggle membership of a single name. */
  toggle(selection: Set<string>, name: string): void
  /** Shift-click / Shift+Arrow: select the contiguous range between anchor and target (inclusive). */
  selectRange(selection: Set<string>, orderedNames: readonly string[], anchor: string, target: string): void
  /** Ctrl+A: select everything currently listed. */
  selectAll(selection: Set<string>, orderedNames: readonly string[]): void
  clear(selection: Set<string>): void
}
import {
  pureClear,
  pureReconcile,
  pureSelectAll,
  pureSelectOnly,
  pureSelectRange,
  pureToggle
} from '../store/slices/selection.slice'


export function selectOnly(selection: Set<string>, name: string): void {
  const next = pureSelectOnly(selection, name)
  selection.clear()
  for (const item of next) selection.add(item)
}

export function toggle(selection: Set<string>, name: string): void {
  const next = pureToggle(selection, name)
  selection.clear()
  for (const item of next) selection.add(item)
}

export function selectRange(
  selection: Set<string>,
  orderedNames: readonly string[],
  anchor: string,
  target: string
): void {
  const next = pureSelectRange(selection, orderedNames, anchor, target)
  selection.clear()
  for (const item of next) selection.add(item)
}

export function selectAll(selection: Set<string>, orderedNames: readonly string[]): void {
  const next = pureSelectAll(orderedNames)
  selection.clear()
  for (const item of next) selection.add(item)
}

export function clear(selection: Set<string>): void {
  const next = pureClear()
  selection.clear()
  for (const item of next) selection.add(item)
}

/**
 * Reconciles a selection after the underlying list changed (refresh,
 * resort, page load). Names no longer present are dropped; the rest survive
 * untouched, by name. This is what makes refresh() safe to call without
 * disturbing what the user has selected.
 */
export function reconcile(selection: Set<string>, stillPresent: ReadonlySet<string>): void {
  const next = pureReconcile(selection, stillPresent)
  selection.clear()
  for (const item of next) selection.add(item)
}
