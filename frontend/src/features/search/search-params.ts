// Search is open while the URL names a scope, on top of whatever page was showing.
import { textParam } from '../../lib/url-search'

export interface SearchParams {
  /** The folder search was opened from; it ranks that subtree up and never confines the search. Empty for none. */
  readonly search?: string
  /** The last submitted query. */
  readonly q?: string
}

export function validateSearchParams(raw: Record<string, unknown>): SearchParams {
  return { search: textParam(raw.search), q: textParam(raw.q) || undefined }
}
