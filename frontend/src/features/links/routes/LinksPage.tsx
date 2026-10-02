import { describeApiError } from '../../../api/error-text'
import { normalizePath } from '../../../lib/path-utils'
import { useI18n } from '../../../hooks/use-i18n'
import { useSession } from '../../auth/api'
import { useAllShareLinks, useShareLinks } from '../api'
import { SecondaryPageShell, SecondaryPageState, secondaryPageStyles, VirtualList } from '@/shared/ui'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { LinkListRow } from '../components/LinkRow'
import { useLinkManagement, type LinkRow } from '../hooks/use-link-management'
import * as styles from './LinksPage.css'

export function LinksPage() {
  const { t } = useI18n()
  const session = useSession()
  const isAdmin = session.data?.user.is_admin === true
  const own = useShareLinks(undefined, session.data !== undefined && !isAdmin)
  const all = useAllShareLinks(isAdmin)
  const management = useLinkManagement(session.data?.user.id)
  const rows: LinkRow[] = isAdmin ? (all.data ?? []) : (own.data ?? [])
  const activeQuery = isAdmin ? all : own
  const loading = session.isPending || activeQuery.isPending
  useDocumentTitle(t('links.title_stowcloud'))

  return (
    <SecondaryPageShell
      title={t('nav.links')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void activeQuery.refetch()}
    >
      <SecondaryPageState
        loading={loading}
        loadingLabel={t('common.loading')}
        error={activeQuery.error}
        errorText={describeApiError(activeQuery.error, t('links.could_not_load'))}
        empty={!loading && rows.length === 0}
        emptyText={t('links.empty')}
      >
        {rows.length > 0 ? (
          <VirtualList
            className={secondaryPageStyles.list}
            items={rows}
            itemKey={(link) => link.id}
            estimateSize={80}
            itemProps={() => ({ className: styles.item })}
            pinnedKeys={management.managingId === null ? undefined : [management.managingId]}
            renderItem={(link) => {
              const mine = management.isMine(link)
              const path = normalizePath(link.path)
              return (
                <>
                  <LinkListRow
                    link={link}
                    mine={mine}
                    resolving={management.resolvingPath === path}
                    targetSummary={management.targetSummary(link)}
                    onOpen={() => void management.openManagement(link)}
                  />
                  {management.targetError?.path === path ? (
                    <p className={styles.targetError} role="alert">
                      {management.targetError.message}
                    </p>
                  ) : null}
                </>
              )
            }}
          />
        ) : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}
