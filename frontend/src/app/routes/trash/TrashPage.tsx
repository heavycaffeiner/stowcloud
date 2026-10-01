import { describeApiError } from '../../../lib/api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { selection } from '../../../lib/store/selection.store'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { VirtualList } from '../../../ui/VirtualList'
import { useDocumentTitle } from '../../hooks/use-document-title'
import { SecondaryPageShell } from '../secondary/SecondaryPageShell'
import { SecondaryPageState } from '../secondary/SecondaryPageState'
import { TrashOperation, TrashRow } from './TrashView'
import { useTrashActions } from './hooks/use-trash-actions'
import { useTrashSelection } from './hooks/use-trash-selection'
import * as styles from './TrashPage.css'
import { useTrash, type TrashEntry } from '../../../features/trash/api'

export function TrashPage() {
  const { t } = useI18n()
  const trash = useTrash()
  const entries: TrashEntry[] = trash.data ?? []
  const selected = useTrashSelection(entries)
  const actions = useTrashActions(t, selected)
  const { state, setState, busy, restorePending, purgePending, restoreItems, requestPurge, cancelPurge, confirmPurge } =
    actions
  const { purgeOpen, purgeSingle, operation, notice } = state
  useDocumentTitle(t('trash.trash_stowcloud'))

  const allSelected = entries.length > 0 && selected.size === entries.length
  const partiallySelected = selected.size > 0 && !allSelected
  const purgeCount = purgeSingle === null ? selected.size : 1

  return (
    <SecondaryPageShell
      title={t('common.trash')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void trash.refetch()}
      overlay={
        <Dialog
          open={purgeOpen}
          title={t('trash.delete_permanently')}
          onClose={cancelPurge}
          actions={
            <>
              <Button variant="text" onClick={cancelPurge}>
                {t('common.cancel')}
              </Button>
              <Button onClick={() => void confirmPurge()}>{t('common.delete')}</Button>
            </>
          }
        >
          <p>{t('trash.permanently_deletes_items_cannot_undone', { count: purgeCount })}</p>
        </Dialog>
      }
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
              onChange={() => (allSelected ? selection.clear() : selection.all(entries.map((entry) => entry.id)))}
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
              onClick={() => requestPurge(null)}
            >
              {t('trash.purge')}
            </Button>
          </div>
        </div>
      ) : null}
      {notice ? (
        <p className={styles.notice} role="status" aria-live="polite">
          {notice}{' '}
          <button type="button" className={styles.noticeClose} onClick={() => setState({ notice: null })}>
            {t('common.close')}
          </button>
        </p>
      ) : null}
      {operation ? <TrashOperation operation={operation} onClose={() => setState({ operation: null })} t={t} /> : null}
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
            pinnedKeys={purgeOpen && purgeSingle !== null ? [purgeSingle] : undefined}
            renderItem={(entry) => (
              <TrashRow
                entry={entry}
                selected={selected.has(entry.id)}
                disabled={busy}
                onToggle={() => selection.toggle(entry.id)}
                onRestore={() => void restoreItems([entry.id])}
                onPurge={() => requestPurge(entry.id)}
                t={t}
              />
            )}
          />
        ) : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}
