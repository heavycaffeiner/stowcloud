import { batchErrorKey } from '../../../api/error-text'
import { formatDateNs } from '../../../lib/i18n'
import { formatBytes } from '../../../lib/format/bytes'
import { Icon } from '../../../ui/Icon'
import { VirtualList } from '../../../ui/VirtualList'
import * as styles from './TrashView.css'
import * as secondaryPageShellStyles from '../../../ui/SecondaryPageShell.css'
import { cx } from '../../../ui/cx'
import type { BatchItemResult } from '../../files/api'
import type { TrashEntry } from '../api'

type Translate = (key: string, params?: Record<string, string | number>) => string

function resultError(result: BatchItemResult, t: Translate): string {
  const key = batchErrorKey(result.error)
  return key ? t(key.key, key.params) : t('error.internal')
}

export function TrashOperation({
  operation,
  onClose,
  t
}: {
  operation: { kind: 'restore' | 'purge'; results: BatchItemResult[] }
  onClose: () => void
  t: Translate
}) {
  return (
    <section className={styles.operation} role="status" aria-live="polite">
      <div className={styles.operationHeading}>
        <h2 className={styles.operationTitle}>
          {operation.kind === 'restore' ? t('trash.restore') : t('trash.purge')}
        </h2>
        <button type="button" className={styles.operationClose} onClick={onClose}>
          {t('common.close')}
        </button>
      </div>
      <VirtualList
        className={styles.operationList}
        items={operation.results}
        itemKey={(result) => result.path}
        estimateSize={20}
        itemProps={(result) => ({
          className: cx(styles.operationItem, !result.ok && styles.operationError)
        })}
        renderItem={(result) => (
          <>
            <span className={styles.operationPath}>{result.path}</span>
            <span className={styles.operationResult}>{result.ok ? t('common.done') : resultError(result, t)}</span>
          </>
        )}
      />
    </section>
  )
}

interface TrashRowProps {
  entry: TrashEntry
  selected: boolean
  disabled: boolean
  onToggle: () => void
  onRestore: () => void
  onPurge: () => void
  t: Translate
}

export function TrashRow({ entry, selected, disabled, onToggle, onRestore, onPurge, t }: TrashRowProps) {
  return (
    <>
      <label className={styles.checkbox}>
        <input
          type="checkbox"
          checked={selected}
          disabled={disabled}
          aria-label={t('common.select', { name: entry.name })}
          onChange={onToggle}
        />
      </label>
      <span className={styles.name} title={entry.name}>
        {entry.name}
      </span>
      {!entry.is_dir ? <span className={styles.meta}>{formatBytes(entry.size)}</span> : null}
      <span className={styles.meta}>{t('trash.deleted', { date: formatDateNs(entry.deleted_at_ns) })}</span>
      <div className={styles.rowActions}>
        <button
          type="button"
          className={secondaryPageShellStyles.routeIconButton}
          disabled={disabled}
          aria-label={t('trash.restore')}
          onClick={onRestore}
        >
          <Icon name="restore" />
        </button>
        <button
          type="button"
          className={cx(secondaryPageShellStyles.routeIconButton, secondaryPageShellStyles.routeIconButtonDanger)}
          disabled={disabled}
          aria-label={t('trash.purge')}
          onClick={onPurge}
        >
          <Icon name="delete" />
        </button>
      </div>
    </>
  )
}
