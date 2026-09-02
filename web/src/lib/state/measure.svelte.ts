// Measuring what a selection actually holds.
//
// A directory entry's own size is the bytes its inode spends on its listing, a
// number like 18 B that says nothing about the tree under it. The real total
// needs a walk, which the server does on request. This holds the result so the
// selection toolbar and the details panel show one answer rather than each
// asking separately.

import { api, ApiError } from '../api/client'
import type { FolderSize } from '../api/types'

/** How long a selection must hold still before its folders are walked.
 *  Arrowing through a listing changes the selection on every keypress, and a
 *  walk per keypress would queue one tree traversal per row passed over. */
const SETTLE_MS = 400

export type MeasureState =
  | { kind: 'idle' }
  | { kind: 'measuring' }
  | { kind: 'done'; bytes: number; files: number }
  | { kind: 'failed'; reason: 'denied' | 'other' }

class SelectionMeasure {
  state = $state<MeasureState>({ kind: 'idle' })

  /** The paths the current answer belongs to. A late reply for a selection
   *  nobody is looking at any more is dropped rather than shown under the new
   *  one's name. */
  #key: string | null = null
  #timer: number | null = null

  /**
   * Points the measurement at a set of folder paths.
   *
   * `base` is what the folders' own sizes are added to: the files in the same
   * selection, already known from the listing. Passing no paths reports that
   * base directly, since there is nothing to walk.
   */
  retarget(paths: string[], base: { bytes: number; files: number }): void {
    // The base is part of the key, not just the paths. Adding a file to a
    // selection leaves the folders untouched, and keying on those alone
    // dropped the retarget and left the previous total on screen without the
    // file in it.
    const key =
      paths.length > 0 || base.files > 0
        ? `${base.bytes}:${base.files}\u0000${paths.join('\u0000')}`
        : null
    if (key === this.#key) return
    this.#key = key
    this.#cancelTimer()

    // Nothing selected at all.
    if (key === null) {
      this.state = { kind: 'idle' }
      return
    }

    // Files only: the listing already carries every size, so there is nothing
    // to walk and nothing to wait for.
    if (paths.length === 0) {
      this.state = { kind: 'done', ...base }
      return
    }

    this.state = { kind: 'measuring' }
    this.#timer = window.setTimeout(() => void this.#run(paths, base, key), SETTLE_MS)
  }

  /** Runs the walk again after a failure, on the paths already targeted. */
  retry(paths: string[], base: { bytes: number; files: number }): void {
    this.#key = null
    this.retarget(paths, base)
  }

  async #run(paths: string[], base: { bytes: number; files: number }, key: string): Promise<void> {
    try {
      // Together rather than one after another: a selection of five cold
      // folders would otherwise wait out five sequential walks.
      const parts = await Promise.all(paths.map((p) => api.folderSize(p)))
      if (this.#key !== key) return
      this.state = {
        kind: 'done',
        bytes: parts.reduce((a, p) => a + p.bytes, base.bytes),
        files: parts.reduce((a, p) => a + p.files, base.files)
      }
    } catch (err) {
      if (this.#key !== key) return
      this.state = {
        kind: 'failed',
        reason: err instanceof ApiError && err.code === 'acl.denied' ? 'denied' : 'other'
      }
    }
  }

  #cancelTimer(): void {
    if (this.#timer !== null) window.clearTimeout(this.#timer)
    this.#timer = null
  }
}

export const selectionMeasure = new SelectionMeasure()
