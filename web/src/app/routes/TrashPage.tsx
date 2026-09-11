import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { formatBytes } from '../../lib/format/bytes'
import { batchErrorKey, describeApiError } from '../../lib/api/error-text'
import type { BatchItemResult, TrashEntry } from '../../lib/api/types'
import { formatDateNs, tp } from '../../lib/i18n'
import { useI18n } from '../../lib/i18n/use-i18n'
import { trashPurgeMutation, trashQuery, trashRestoreMutation } from '../../lib/query/files'
import { selection } from '../../lib/store/selection.store'
import { useStore } from '../../lib/store/use-store'
import { Button } from '../../lib/ui/Button'
import { useDocumentTitle } from '../use-document-title'
import './simple-pages.css'
import { Icon } from '../../lib/ui/Icon'

function resultError(result: BatchItemResult, t: (key: string, params?: Record<string, string | number>) => string): string {
  const key = batchErrorKey(result.error)
  return key ? t(key.key, key.params) : t('error.internal')
}

export function TrashPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const trash = useQuery(trashQuery())
  const restore = useMutation(trashRestoreMutation())
  const purge = useMutation(trashPurgeMutation())
  const entries: TrashEntry[] = trash.data ?? []
  const selected = useStore(selection, (state) => state.names)
  const [purgeOpen, setPurgeOpen] = useState(false)
  const purgeDialogRef = useRef<HTMLElement | null>(null)
  const [purgeSingle, setPurgeSingle] = useState<string | null>(null)
  const [operation, setOperation] = useState<{ kind: 'restore' | 'purge'; results: BatchItemResult[] } | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  useDocumentTitle(t('trash.trash_stowcloud'))
  useEffect(() => {
    const element = purgeDialogRef.current
    if (!element) return
    const close = () => {
      setPurgeOpen(false)
      setPurgeSingle(null)
    }
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [])

  useEffect(() => {
    const known = new Set(entries.map((entry) => entry.id))
    const next = new Set([...selected].filter((id) => known.has(id)))
    if (next.size !== selected.size) selection.replace(next)
  }, [entries, selected])

  function summarize(results: readonly BatchItemResult[], verb: string): string {
    const failed = results.filter((result) => !result.ok).length
    if (failed === 0) return tp('trash.items', results.length, { verb })
    return t('trash.succeeded_failed', { verb, ok: results.length - failed, failed })
  }

  function applyResults(ids: readonly string[], results: readonly BatchItemResult[]): void {
    const failed = new Set(results.filter((result) => !result.ok).map((result) => result.path))
    selection.replace([...selected].filter((id) => !ids.includes(id) || failed.has(id)))
  }

  async function restoreItems(ids: string[]): Promise<void> {
    if (ids.length === 0) return
    try {
      const response = await restore.mutateAsync(ids)
      setOperation({ kind: 'restore', results: response.results })
      setNotice(summarize(response.results, t('trash.restored')))
      applyResults(ids, response.results)
    } catch (error) {
      setNotice(describeApiError(error, t('trash.could_not_restore')))
    }
  }

  function requestPurge(id: string | null): void {
    if (id === null && selected.size === 0) return
    setPurgeSingle(id)
    setPurgeOpen(true)
  }

  async function confirmPurge(): Promise<void> {
    const ids = purgeSingle === null ? [...selected] : [purgeSingle]
    setPurgeOpen(false)
    setPurgeSingle(null)
    if (ids.length === 0) return
    try {
      const response = await purge.mutateAsync(ids)
      setOperation({ kind: 'purge', results: response.results })
      setNotice(summarize(response.results, t('trash.deleted_permanently')))
      applyResults(ids, response.results)
    } catch (error) {
      setNotice(describeApiError(error, t('trash.could_not_delete_permanently')))
    }
  }

  const busy = restore.isPending || purge.isPending
  const allSelected = entries.length > 0 && selected.size === entries.length
  const partiallySelected = selected.size > 0 && !allSelected
  const purgeCount = purgeSingle === null ? selected.size : 1

  return (
    <section className="sc-trash sc-secondary-page">
      <div className="sc-secondary-page__inner">
        <header className="sc-secondary-page__header">
          <button type="button" className="sc-route-back" aria-label={t('trash.go_back')} onClick={() => void navigate('/b')}><Icon name="chevron_left" /></button>
          <h1>{t('common.trash')}</h1>
          <button type="button" className="sc-route-icon-button" aria-label={t('common.refresh')} onClick={() => void trash.refetch()}><Icon name="refresh" /></button>
        </header>
        {entries.length > 0 ? (
          <div className="sc-trash__toolbar">
            <label className="sc-trash__select-all">
              <input type="checkbox" checked={allSelected} ref={(element) => { if (element) element.indeterminate = partiallySelected }} onChange={() => allSelected ? selection.clear() : selection.all(entries.map((entry) => entry.id))} />
              {t('trash.select_all', { selected: selected.size, total: entries.length })}
            </label>
            <div className="sc-trash__toolbar-actions">
              <Button variant="text" disabled={selected.size === 0 || busy} loading={restore.isPending} onClick={() => void restoreItems([...selected])}>{t('trash.restore')}</Button>
              <Button variant="text" danger disabled={selected.size === 0 || busy} loading={purge.isPending} onClick={() => requestPurge(null)}>{t('trash.purge')}</Button>
            </div>
          </div>
        ) : null}
        {notice ? <p className="sc-trash__notice" role="status" aria-live="polite">{notice} <button type="button" onClick={() => setNotice(null)}>{t('common.close')}</button></p> : null}
        {operation ? <section className="sc-trash__operation" role="status" aria-live="polite"><div className="sc-trash__operation-heading"><h2>{operation.kind === 'restore' ? t('trash.restore') : t('trash.purge')}</h2><button type="button" onClick={() => setOperation(null)}>{t('common.close')}</button></div><ul>{operation.results.map((result) => <li key={result.path} className={result.ok ? undefined : 'error'}><span>{result.path}</span><span>{result.ok ? t('common.done') : resultError(result, t)}</span></li>)}</ul></section> : null}
        {trash.isPending ? <div className="sc-secondary-page__loading"><mdui-circular-progress></mdui-circular-progress></div> : null}
        {trash.error ? <p className="sc-secondary-page__error" role="alert">{describeApiError(trash.error, t('trash.could_not_load_trash'))}</p> : null}
        {!trash.isPending && !trash.error && entries.length === 0 ? <p className="sc-secondary-page__empty">{t('trash.trash_empty')}</p> : null}
        {entries.length > 0 ? <ul className="sc-secondary-page__list">{entries.map((entry) => <TrashRow key={entry.id} entry={entry} selected={selected.has(entry.id)} disabled={busy} onToggle={() => selection.toggle(entry.id)} onRestore={() => void restoreItems([entry.id])} onPurge={() => requestPurge(entry.id)} t={t} />)}</ul> : null}
      </div>
      <mdui-dialog ref={purgeDialogRef} open={purgeOpen} headline={t('trash.delete_permanently')} close-on-esc>
        <p>{t('trash.permanently_deletes_items_cannot_undone', { count: purgeCount })}</p>
        <mdui-button slot="action" variant="text" onClick={() => setPurgeOpen(false)}>{t('common.cancel')}</mdui-button>
        <mdui-button slot="action" variant="filled" onClick={() => void confirmPurge()}>{t('common.delete')}</mdui-button>
      </mdui-dialog>
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
  t: (key: string, params?: Record<string, string | number>) => string
}
 
function TrashRow({ entry, selected, disabled, onToggle, onRestore, onPurge, t }: TrashRowProps) {
  return <li className="sc-trash__row"><label className="sc-trash__checkbox"><input type="checkbox" checked={selected} disabled={disabled} aria-label={t('common.select', { name: entry.name })} onChange={onToggle} /></label><span className="sc-trash__name" title={entry.name}>{entry.name}</span>{!entry.is_dir ? <span className="sc-trash__meta">{formatBytes(entry.size)}</span> : null}<span className="sc-trash__meta">{t('trash.deleted', { date: formatDateNs(entry.deleted_at_ns) })}</span><div className="sc-trash__row-actions"><button type="button" className="sc-route-icon-button" disabled={disabled} aria-label={t('trash.restore')} onClick={onRestore}><Icon name="restore" /></button><button type="button" className="sc-route-icon-button sc-route-icon-button--danger" disabled={disabled} aria-label={t('trash.purge')} onClick={onPurge}><Icon name="delete" /></button></div></li>
}
