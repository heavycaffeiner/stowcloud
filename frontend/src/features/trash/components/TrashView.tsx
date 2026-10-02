import { overlay } from 'overlay-kit'
import { batchErrorKey } from '../../../api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { formatDateNs, t } from '../../../i18n'
import { formatBytes } from '../../../lib/format/bytes'
import { cx, StowButton, StowDialog, StowIconButton, VirtualList } from '@/shared/ui'
import * as styles from './TrashView.css'
import type { BatchItemResult } from '../../file-browser/api'
import type { TrashEntry } from '../api'
import type { TrashOperationResult } from '../hooks/use-trash-actions'

/** Asks to confirm a permanent delete of `count` items. True when confirmed. */
export function confirmPurge(count: number): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <StowDialog
      open={isOpen}
      title={t('trash.delete_permanently')}
      onClose={() => close(false)}
      onClosed={unmount}
      actions={
        <>
          <StowButton variant="text" onClick={() => close(false)}>
            {t('common.cancel')}
          </StowButton>
          <StowButton onClick={() => close(true)}>{t('common.delete')}</StowButton>
        </>
      }
    >
      <p>{t('trash.permanently_deletes_items_cannot_undone', { count })}</p>
    </StowDialog>
  ))
}

function resultError(result: BatchItemResult): string {
  const key = batchErrorKey(result.error)
  return key ? t(key.key, key.params) : t('error.internal')
}

export function TrashOperation({ operation, onClose }: { operation: TrashOperationResult; onClose: () => void }) {
  const { t } = useI18n()
  return (
    <section className={styles.operation} role="status" aria-live="polite">
      <div className={styles.operationHeading}>
        <h2 className={styles.operationTitle}>
          {operation.kind === 'restore' ? t('trash.restore') : t('trash.purge')}
        </h2>
        <StowButton variant="text" onClick={onClose}>
          {t('common.close')}
        </StowButton>
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
            <span className={styles.operationResult}>{result.ok ? t('common.done') : resultError(result)}</span>
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
}

export function TrashRow({ entry, selected, disabled, onToggle, onRestore, onPurge }: TrashRowProps) {
  const { t } = useI18n()
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
        <StowIconButton label={t('trash.restore')} icon="restore" disabled={disabled} onClick={onRestore} />
        <StowIconButton label={t('trash.purge')} icon="delete" danger disabled={disabled} onClick={onPurge} />
      </div>
    </>
  )
}
