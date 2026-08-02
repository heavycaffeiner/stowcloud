// web/src/lib/state/selection.ts — pure selection-model logic.
// Selection is kept by name, never by index — so a
// list refresh never strands the selection on the wrong row. Kept as plain
// functions over a Set<string> so it is trivially unit-testable and so the
// app layer can wrap it around a SvelteSet for reactivity (browse.svelte.ts).

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

export function selectOnly(selection: Set<string>, name: string): void {
  selection.clear()
  selection.add(name)
}

export function toggle(selection: Set<string>, name: string): void {
  if (selection.has(name)) selection.delete(name)
  else selection.add(name)
}

export function selectRange(
  selection: Set<string>,
  orderedNames: readonly string[],
  anchor: string,
  target: string
): void {
  const ai = orderedNames.indexOf(anchor)
  const ti = orderedNames.indexOf(target)
  if (ai === -1 || ti === -1) {
    selectOnly(selection, target)
    return
  }
  const [lo, hi] = ai <= ti ? [ai, ti] : [ti, ai]
  selection.clear()
  for (let i = lo; i <= hi; i++) selection.add(orderedNames[i])
}

export function selectAll(selection: Set<string>, orderedNames: readonly string[]): void {
  selection.clear()
  for (const n of orderedNames) selection.add(n)
}

export function clear(selection: Set<string>): void {
  selection.clear()
}

/**
 * Reconciles a selection after the underlying list changed (refresh,
 * resort, page load). Names no longer present are dropped; the rest survive
 * untouched, by name. This is what makes refresh() safe to call without
 * disturbing what the user has selected.
 */
export function reconcile(selection: Set<string>, stillPresent: ReadonlySet<string>): void {
  for (const name of selection) {
    if (!stillPresent.has(name)) selection.delete(name)
  }
}
