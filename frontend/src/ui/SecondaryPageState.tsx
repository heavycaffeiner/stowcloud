import type { ReactNode } from 'react'
import { ProgressCircular } from './ProgressCircular'
import * as secondaryPageShellStyles from './SecondaryPageShell.css'

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
export function SecondaryPageState({
  loading,
  loadingLabel,
  error,
  errorText,
  empty,
  emptyText,
  children
}: SecondaryPageStateProps) {
  return (
    <>
      {loading ? (
        <div className={secondaryPageShellStyles.loading} role="status" aria-live="polite">
          <ProgressCircular size={40} label={loadingLabel} />
        </div>
      ) : null}
      {error ? (
        <p className={secondaryPageShellStyles.error} role="alert">
          {errorText}
        </p>
      ) : null}
      {!loading && !error && empty ? <p className={secondaryPageShellStyles.empty}>{emptyText}</p> : null}
      {children}
    </>
  )
}
