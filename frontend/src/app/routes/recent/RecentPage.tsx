import { useNavigate } from 'react-router-dom'
import { describeApiError } from '../../../lib/api/error-text'
import { formatBytes } from '../../../lib/format/bytes'
import { formatDateNs } from '../../../lib/i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { normalizePath, parentOf } from '../../../lib/api/path-utils'
import { useDocumentTitle } from '../../hooks/use-document-title'
import { Icon } from '../../../ui/Icon'
import { VirtualList } from '../../../ui/VirtualList'
import { SecondaryPageShell } from '../secondary/SecondaryPageShell'
import { SecondaryPageState } from '../secondary/SecondaryPageState'
import * as styles from './RecentPage.css'
import * as secondaryPageShellStyles from '../secondary/SecondaryPageShell.css'
import { useRecent, type RecentHit } from '../../../features/recent/api'

const RECENT_LIMIT = 100
const RECENT_RETENTION_DAYS = 14

function parentOfVpath(vpath: string): string {
  return parentOf(normalizePath(vpath))
}

export function RecentPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const recent = useRecent(RECENT_LIMIT)
  const hits = recent.data ?? []
  useDocumentTitle(t('recent.title_stowcloud'))

  function verb(hit: RecentHit): string {
    switch (hit.op) {
      case 'edit':
        return t('recent.op_edit')
      case 'copy':
        return t('recent.op_copy')
      case 'move':
        return t('recent.op_move')
      case 'restore':
        return t('recent.op_restore')
      default:
        return t('recent.op_upload')
    }
  }

  return (
    <SecondaryPageShell
      className={styles.root}
      title={t('nav.recent')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void recent.refetch()}
    >
      <p className={secondaryPageShellStyles.coverage}>
        {t('recent.coverage', { limit: RECENT_LIMIT, days: RECENT_RETENTION_DAYS })}
      </p>
      <SecondaryPageState
        loading={recent.isPending}
        loadingLabel={t('common.loading')}
        error={recent.error}
        errorText={describeApiError(recent.error, t('recent.could_not_load'))}
        empty={!recent.isPending && hits.length === 0}
        emptyText={t('recent.nothing_recent')}
      >
        {hits.length > 0 ? (
          <VirtualList
            className={secondaryPageShellStyles.list}
            items={hits}
            itemKey={(hit) => `${hit.at_ns}:${hit.vpath}`}
            estimateSize={56}
            renderItem={(hit) => {
              const parent = parentOfVpath(hit.vpath)
              const href = `${parent === '/' ? '/b' : `/b${parent}`}?focus=${encodeURIComponent(hit.name)}`
              return (
                <button
                  type="button"
                  className={secondaryPageShellStyles.row}
                  aria-label={t('recent.open_item', { name: hit.name, folder: parent })}
                  onClick={() => void navigate(href)}
                >
                  <span className={secondaryPageShellStyles.icon}>
                    <Icon name="draft" />
                  </span>
                  <span className={secondaryPageShellStyles.text}>
                    <span className={secondaryPageShellStyles.name}>{hit.name}</span>
                    <span className={secondaryPageShellStyles.path}>{parent}</span>
                  </span>
                  <span className={secondaryPageShellStyles.meta}>{verb(hit)}</span>
                  <span className={secondaryPageShellStyles.meta}>{formatBytes(hit.size)}</span>
                  <span className={secondaryPageShellStyles.meta}>{formatDateNs(hit.at_ns)}</span>
                </button>
              )
            }}
          />
        ) : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}
