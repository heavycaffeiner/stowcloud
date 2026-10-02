import type { ReactNode } from 'react'
import { StowProgressCircular } from '../Progress'
import * as styles from './SecondaryPage.css'

export interface SecondaryPageStateProps {
  loading: boolean
  loadingLabel: string
  error: unknown
  errorText: string
  empty: boolean
  emptyText: string
  children?: ReactNode
}

/** The loading, error and empty states of a secondary route. */
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
        <div className={styles.state} role="status" aria-live="polite">
          <StowProgressCircular size={40} label={loadingLabel} />
        </div>
      ) : null}
      {error ? (
        <p className={styles.error} role="alert">
          {errorText}
        </p>
      ) : null}
      {!loading && !error && empty ? <p className={styles.state}>{emptyText}</p> : null}
      {children}
    </>
  )
}
