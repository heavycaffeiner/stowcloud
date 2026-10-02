import type { ReactNode } from 'react'
import { Component } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { StowButton } from '../Button'
import * as styles from './ErrorBoundary.css'

export interface ErrorBoundaryProps {
  children?: ReactNode
  /** A new value clears a caught crash and renders the children again. */
  resetKey?: unknown
  /** Shown instead of the default notice, for a region that has to keep its own frame, such as a dialog. */
  fallback?: ReactNode
}

interface ErrorBoundaryState {
  failed: boolean
  resetKey: unknown
}

/** Keeps a crash inside one region of the page so the rest of the app stays usable. */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false, resetKey: this.props.resetKey }

  static getDerivedStateFromError(): Partial<ErrorBoundaryState> {
    return { failed: true }
  }

  static getDerivedStateFromProps(props: ErrorBoundaryProps, state: ErrorBoundaryState): ErrorBoundaryState | null {
    return Object.is(props.resetKey, state.resetKey) ? null : { failed: false, resetKey: props.resetKey }
  }

  render() {
    if (!this.state.failed) return this.props.children
    return this.props.fallback ?? <ErrorNotice onRetry={() => this.setState({ failed: false })} />
  }
}

function ErrorNotice({ onRetry }: { onRetry: () => void }) {
  const { t } = useI18n()
  return (
    <div className={styles.notice} role="alert">
      <p className={styles.message}>{t('error.section_failed')}</p>
      <StowButton variant="text" onClick={onRetry}>
        {t('common.retry')}
      </StowButton>
    </div>
  )
}
