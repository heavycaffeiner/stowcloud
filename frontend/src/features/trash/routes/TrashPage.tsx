import { describeApiError } from '../../../api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { VirtualList } from '../../../ui/VirtualList'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { SecondaryPageShell } from '../../../ui/SecondaryPageShell'
import { SecondaryPageState } from '../../../ui/SecondaryPageState'
import { TrashOperation, TrashRow } from './TrashView'
import { useTrashActions } from '../hooks/use-trash-actions'
import { useTrashSelection } from '../hooks/use-trash-selection'
import * as styles from './TrashPage.css'
import { useTrash, type TrashEntry } from '../api'

export function TrashPage() {
  const { t } = useI18n()
  const trash = useTrash()
  const entries: TrashEntry[] = trash.data ?? []
  const selection = useTrashSelection(entries)
  const { selected } = selection
  const {
    operation,
    notice,
    pinned,
    busy,
    restorePending,
    purgePending,
    restoreItems,
    purgeItems,
    dismissOperation,
    dismissNotice
  } = useTrashActions(selection)
  useDocumentTitle(t('trash.trash_stowcloud'))

  const allSelected = entries.length > 0 && selected.size === entries.length
  const partiallySelected = selected.size > 0 && !allSelected

  return (
    <SecondaryPageShell
      title={t('common.trash')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void trash.refetch()}
    >
      {entries.length > 0 ? (
        <div className={styles.toolbar}>
          <label className={styles.selectAll}>
            <input
              type="checkbox"
              checked={allSelected}
              ref={(element) => {
                if (element) element.indeterminate = partiallySelected
              }}
              onChange={() => (allSelected ? selection.clear() : selection.setAll(entries.map((entry) => entry.id)))}
            />
            {t('trash.select_all', { selected: selected.size, total: entries.length })}
          </label>
          <div className={styles.toolbarActions}>
            <Button
              variant="text"
              disabled={selected.size === 0 || busy}
              loading={restorePending}
              onClick={() => void restoreItems([...selected])}
            >
              {t('trash.restore')}
            </Button>
            <Button
              variant="text"
              danger
              disabled={selected.size === 0 || busy}
              loading={purgePending}
              onClick={() => void purgeItems(null)}
            >
              {t('trash.purge')}
            </Button>
          </div>
        </div>
      ) : null}
      {notice ? (
        <p className={styles.notice} role="status" aria-live="polite">
          {notice}{' '}
          <button type="button" className={styles.noticeClose} onClick={dismissNotice}>
            {t('common.close')}
          </button>
        </p>
      ) : null}
      {operation ? <TrashOperation operation={operation} onClose={dismissOperation} /> : null}
      <SecondaryPageState
        loading={trash.isPending}
        loadingLabel={t('common.loading')}
        error={trash.error}
        errorText={describeApiError(trash.error, t('trash.could_not_load_trash'))}
        empty={!trash.isPending && entries.length === 0}
        emptyText={t('trash.trash_empty')}
      >
        {entries.length > 0 ? (
          <VirtualList
            items={entries}
            itemKey={(entry) => entry.id}
            estimateSize={61}
            itemProps={() => ({ className: styles.row })}
            pinnedKeys={pinned === null ? undefined : [pinned]}
            renderItem={(entry) => (
              <TrashRow
                entry={entry}
                selected={selected.has(entry.id)}
                disabled={busy}
                onToggle={() => selection.toggle(entry.id)}
                onRestore={() => void restoreItems([entry.id])}
                onPurge={() => void purgeItems(entry.id)}
              />
            )}
          />
        ) : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}
