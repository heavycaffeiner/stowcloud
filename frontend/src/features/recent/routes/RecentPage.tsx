import { useNavigate } from '@tanstack/react-router'
import { describeApiError } from '../../../api/error-text'
import { formatBytes } from '../../../lib/format/bytes'
import { formatDateNs } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { normalizePath, parentOf } from '../../../lib/path-utils'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { Icon, SecondaryPageShell, SecondaryPageState, secondaryPageStyles, VirtualList } from '@/shared/ui'
import * as styles from './RecentPage.css'
import { useRecent, type RecentHit } from '../api'
import { splatOf } from '../../file-browser/browse-search'

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
      title={t('nav.recent')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void recent.refetch()}
    >
      <p className={styles.coverage}>{t('recent.coverage', { limit: RECENT_LIMIT, days: RECENT_RETENTION_DAYS })}</p>
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
            className={secondaryPageStyles.list}
            items={hits}
            itemKey={(hit) => `${hit.at_ns}:${hit.vpath}`}
            estimateSize={56}
            renderItem={(hit) => {
              const parent = parentOfVpath(hit.vpath)
              return (
                <button
                  type="button"
                  className={secondaryPageStyles.row}
                  aria-label={t('recent.open_item', { name: hit.name, folder: parent })}
                  onClick={() => void navigate({ to: '/b/$', params: splatOf(parent), search: { focus: hit.name } })}
                >
                  <span className={secondaryPageStyles.icon}>
                    <Icon name="draft" />
                  </span>
                  <span className={secondaryPageStyles.text}>
                    <span className={secondaryPageStyles.name}>{hit.name}</span>
                    <span className={secondaryPageStyles.path}>{parent}</span>
                  </span>
                  <span className={styles.meta}>{verb(hit)}</span>
                  <span className={styles.meta}>{formatBytes(hit.size)}</span>
                  <span className={styles.meta}>{formatDateNs(hit.at_ns)}</span>
                </button>
              )
            }}
          />
        ) : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}
