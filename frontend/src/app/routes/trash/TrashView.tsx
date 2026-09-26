import type { BatchItemResult, TrashEntry } from '../../../lib/api/types'
import { batchErrorKey } from '../../../lib/api/error-text'
import { formatDateNs } from '../../../lib/i18n'
import { formatBytes } from '../../../lib/format/bytes'
import { Icon } from '../../../lib/ui/Icon'
import { VirtualList } from '../../../lib/ui/VirtualList'

type Translate = (key: string, params?: Record<string, string | number>) => string

function resultError(result: BatchItemResult, t: Translate): string {
  const key = batchErrorKey(result.error)
  return key ? t(key.key, key.params) : t('error.internal')
}

export function TrashOperation({ operation, onClose, t }: { operation: { kind: 'restore' | 'purge'; results: BatchItemResult[] }; onClose: () => void; t: Translate }) {
  return (
    <section className="sc-trash-operation" role="status" aria-live="polite">
      <div className="sc-trash-operation-heading">
        <h2>{operation.kind === 'restore' ? t('trash.restore') : t('trash.purge')}</h2>
        <button type="button" onClick={onClose}>{t('common.close')}</button>
      </div>
      <VirtualList items={operation.results} itemKey={(result) => result.path} estimateSize={20} itemProps={(result) => ({ className: result.ok ? undefined : 'error' })} renderItem={(result) => <><span>{result.path}</span><span>{result.ok ? t('common.done') : resultError(result, t)}</span></>} />
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
  return <><label className="sc-trash-checkbox"><input type="checkbox" checked={selected} disabled={disabled} aria-label={t('common.select', { name: entry.name })} onChange={onToggle} /></label><span className="sc-trash-name" title={entry.name}>{entry.name}</span>{!entry.is_dir ? <span className="sc-trash-meta">{formatBytes(entry.size)}</span> : null}<span className="sc-trash-meta">{t('trash.deleted', { date: formatDateNs(entry.deleted_at_ns) })}</span><div className="sc-trash-row-actions"><button type="button" className="sc-route-icon-button" disabled={disabled} aria-label={t('trash.restore')} onClick={onRestore}><Icon name="restore" /></button><button type="button" className="sc-route-icon-button sc-route-icon-button--danger" disabled={disabled} aria-label={t('trash.purge')} onClick={onPurge}><Icon name="delete" /></button></div></>
}
