import type { ReactNode } from 'react'

export interface SecondaryPageStateProps {
  loading: boolean
  loadingLabel: string
  error: unknown
  errorText: string
  empty: boolean
  emptyText: string
  children?: ReactNode
}

/** Shared loading, error, and empty-state semantics for secondary routes. */
export function SecondaryPageState({ loading, loadingLabel, error, errorText, empty, emptyText, children }: SecondaryPageStateProps) {
  return (
    <>
      {loading ? <div className="sc-secondary-page__loading" role="status" aria-live="polite"><mdui-circular-progress aria-label={loadingLabel}></mdui-circular-progress></div> : null}
      {error ? <p className="sc-secondary-page__error" role="alert">{errorText}</p> : null}
      {!loading && !error && empty ? <p className="sc-secondary-page__empty">{emptyText}</p> : null}
      {children}
    </>
  )
}
