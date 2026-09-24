import { useQuery } from '@tanstack/react-query'
import { describeApiError } from '../../../lib/api/error-text'
import type { TrashEntry } from '../../../lib/api/types'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { trashQuery } from '../../../lib/query/files'
import { selection } from '../../../lib/store/selection.store'
import { Button } from '../../../lib/ui/Button'
import { VirtualList } from '../../../lib/ui/VirtualList'
import { useDocumentTitle } from '../../use-document-title'
import { SecondaryPageShell } from '../secondary/SecondaryPageShell'
import { SecondaryPageState } from '../secondary/SecondaryPageState'
import { TrashOperation, TrashRow } from './TrashView'
import { useTrashActions } from './use-trash-actions'
import { useTrashSelection } from './use-trash-selection'
import '../simple-pages.css'

export function TrashPage() {
  const { t } = useI18n()
  const trash = useQuery(trashQuery())
  const entries: TrashEntry[] = trash.data ?? []
  const selected = useTrashSelection(entries)
  const actions = useTrashActions(t, selected)
  const { state, setState, purgeDialogRef, busy, restorePending, purgePending, restoreItems, requestPurge, confirmPurge } = actions
  const { purgeOpen, purgeSingle, operation, notice } = state
  useDocumentTitle(t('trash.trash_stowcloud'))

  const allSelected = entries.length > 0 && selected.size === entries.length
  const partiallySelected = selected.size > 0 && !allSelected
  const purgeCount = purgeSingle === null ? selected.size : 1

  return (
    <SecondaryPageShell className="sc-trash" title={t('common.trash')} refreshLabel={t('common.refresh')} onRefresh={() => void trash.refetch()} overlay={<mdui-dialog ref={purgeDialogRef} open={purgeOpen} headline={t('trash.delete_permanently')} close-on-esc>
      <p>{t('trash.permanently_deletes_items_cannot_undone', { count: purgeCount })}</p>
      <mdui-button slot="action" variant="text" onClick={() => setState({ purgeOpen: false })}>{t('common.cancel')}</mdui-button>
      <mdui-button slot="action" variant="filled" onClick={() => void confirmPurge()}>{t('common.delete')}</mdui-button>
    </mdui-dialog>}>
      {entries.length > 0 ? <div className="sc-trash__toolbar">
        <label className="sc-trash__select-all">
          <input type="checkbox" checked={allSelected} ref={(element) => { if (element) element.indeterminate = partiallySelected }} onChange={() => allSelected ? selection.clear() : selection.all(entries.map((entry) => entry.id))} />
          {t('trash.select_all', { selected: selected.size, total: entries.length })}
        </label>
        <div className="sc-trash__toolbar-actions">
          <Button variant="text" disabled={selected.size === 0 || busy} loading={restorePending} onClick={() => void restoreItems([...selected])}>{t('trash.restore')}</Button>
          <Button variant="text" danger disabled={selected.size === 0 || busy} loading={purgePending} onClick={() => requestPurge(null)}>{t('trash.purge')}</Button>
        </div>
      </div> : null}
      {notice ? <p className="sc-trash__notice" role="status" aria-live="polite">{notice} <button type="button" onClick={() => setState({ notice: null })}>{t('common.close')}</button></p> : null}
      {operation ? <TrashOperation operation={operation} onClose={() => setState({ operation: null })} t={t} /> : null}
      <SecondaryPageState loading={trash.isPending} loadingLabel={t('common.loading')} error={trash.error} errorText={describeApiError(trash.error, t('trash.could_not_load_trash'))} empty={!trash.isPending && entries.length === 0} emptyText={t('trash.trash_empty')}>
        {entries.length > 0 ? <VirtualList className="sc-trash__list" items={entries} itemKey={(entry) => entry.id} estimateSize={61} itemProps={() => ({ className: 'sc-trash__row' })} pinnedKeys={purgeOpen && purgeSingle !== null ? [purgeSingle] : undefined} renderItem={(entry) => <TrashRow entry={entry} selected={selected.has(entry.id)} disabled={busy} onToggle={() => selection.toggle(entry.id)} onRestore={() => void restoreItems([entry.id])} onPurge={() => requestPurge(entry.id)} t={t} />} /> : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}