import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import type { RecentHit } from '../../../lib/api/types'
import { describeApiError } from '../../../lib/api/error-text'
import { formatBytes } from '../../../lib/format/bytes'
import { formatDateNs } from '../../../lib/i18n'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { normalizePath, parentOf } from '../../../lib/api/path-utils'
import { recentQuery } from '../../../lib/query/files'
import { useDocumentTitle } from '../../use-document-title'
import { Icon } from '../../../lib/ui/Icon'
import { VirtualList } from '../../../lib/ui/VirtualList'
import { SecondaryPageShell } from '../secondary/SecondaryPageShell'
import { SecondaryPageState } from '../secondary/SecondaryPageState'
import '../simple-pages.css'

const RECENT_LIMIT = 100
const RECENT_RETENTION_DAYS = 14

function parentOfVpath(vpath: string): string {
  return parentOf(normalizePath(vpath))
}

export function RecentPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const recent = useQuery(recentQuery(RECENT_LIMIT))
  const hits = recent.data?.hits ?? []
  useDocumentTitle(t('recent.title_stowcloud'))

  function verb(hit: RecentHit): string {
    switch (hit.op) {
      case 'edit': return t('recent.op_edit')
      case 'copy': return t('recent.op_copy')
      case 'move': return t('recent.op_move')
      case 'restore': return t('recent.op_restore')
      default: return t('recent.op_upload')
    }
  }

  return (
    <SecondaryPageShell className="sc-recent" title={t('nav.recent')} refreshLabel={t('common.refresh')} onRefresh={() => void recent.refetch()}>
      <p className="sc-secondary-page__coverage">{t('recent.coverage', { limit: RECENT_LIMIT, days: RECENT_RETENTION_DAYS })}</p>
      <SecondaryPageState loading={recent.isPending} loadingLabel={t('common.loading')} error={recent.error} errorText={describeApiError(recent.error, t('recent.could_not_load'))} empty={!recent.isPending && hits.length === 0} emptyText={t('recent.nothing_recent')}>
        {hits.length > 0 ? (
          <VirtualList className="sc-secondary-page__list" items={hits} itemKey={(hit) => `${hit.at_ns}:${hit.vpath}`} estimateSize={56} renderItem={(hit) => {
            const parent = parentOfVpath(hit.vpath)
            const href = `${parent === '/' ? '/b' : `/b${parent}`}?focus=${encodeURIComponent(hit.name)}`
            return <button type="button" className="sc-secondary-page__row sc-recent__row" aria-label={t('recent.open_item', { name: hit.name, folder: parent })} onClick={() => void navigate(href)}><span className="sc-secondary-page__icon"><Icon name="draft" /></span><span className="sc-secondary-page__text"><span className="sc-secondary-page__name">{hit.name}</span><span className="sc-secondary-page__path">{parent}</span></span><span className="sc-secondary-page__meta">{verb(hit)}</span><span className="sc-secondary-page__meta">{formatBytes(hit.size)}</span><span className="sc-secondary-page__meta">{formatDateNs(hit.at_ns)}</span></button>
          }} />
        ) : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}