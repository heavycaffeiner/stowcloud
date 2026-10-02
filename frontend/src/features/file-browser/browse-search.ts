// What a folder URL carries beside the path. Defaults stay out of the URL, so
// an unfiltered folder has one address.
import { normalizePath } from '../../lib/path-utils'
import { choiceParam, textParam } from '../../lib/url-search'
import { FILTER_DATES, FILTER_TYPES, type BrowseFilterDate, type BrowseFilterType } from './model/filtering'

export interface BrowseSearch {
  readonly type?: BrowseFilterType
  readonly date?: BrowseFilterDate
  /** The file shown in the preview. */
  readonly preview?: string
  /** An entry to scroll to and focus once the listing has it. */
  readonly focus?: string
}

export function validateBrowseSearch(raw: Record<string, unknown>): BrowseSearch {
  return {
    type: choiceParam(FILTER_TYPES, raw.type),
    date: choiceParam(FILTER_DATES, raw.date),
    preview: textParam(raw.preview) || undefined,
    focus: textParam(raw.focus) || undefined
  }
}

/** The route params for the folder or file at `path`. */
export function splatOf(path: string): { _splat: string } {
  return { _splat: path.replace(/^\/+/, '') }
}

/** The folder or file a route's splat names. */
export function splatPath(splat: string | undefined): string {
  return normalizePath(`/${splat ?? ''}`)
}
