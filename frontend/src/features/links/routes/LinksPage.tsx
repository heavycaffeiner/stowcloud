import { describeApiError } from '../../../api/error-text'
import { baseName, normalizePath } from '../../../lib/path-utils'
import { useI18n } from '../../../hooks/use-i18n'
import { useSession } from '../../auth/api'
import { useAllShareLinks, useShareLinks } from '../api'
import { VirtualList } from '../../../ui/VirtualList'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { SecondaryPageShell } from '../../../ui/SecondaryPageShell'
import { SecondaryPageState } from '../../../ui/SecondaryPageState'
import { LinkListRow } from './LinkRow'
import { useLinkManagement, type LinkRow } from '../hooks/use-link-management'
import { ShareManageDialog } from '../../shares/ShareManageDialog'
import * as styles from './LinksPage.css'
import * as secondaryPageShellStyles from '../../../ui/SecondaryPageShell.css'

export function LinksPage() {
  const { t } = useI18n()
  const session = useSession()
  const isAdmin = session.data?.user.is_admin === true
  const own = useShareLinks(undefined, session.data !== undefined && !isAdmin)
  const all = useAllShareLinks(isAdmin)
  const management = useLinkManagement(session.data?.user.id, t)
  const rows: LinkRow[] = isAdmin ? (all.data ?? []) : (own.data ?? [])
  const activeQuery = isAdmin ? all : own
  const loading = session.isPending || activeQuery.isPending
  useDocumentTitle(t('links.title_stowcloud'))

  return (
    <SecondaryPageShell
      title={t('nav.links')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void activeQuery.refetch()}
      overlay={
        management.managing ? (
          <ShareManageDialog
            open
            path={normalizePath(management.managing.path)}
            targetName={baseName(management.managing.path) || management.managing.path}
            targetIsDir={management.managingTarget?.kind === 'dir'}
            onClose={() => management.setState({ managing: null, managingTarget: null })}
          />
        ) : null
      }
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
            className={secondaryPageShellStyles.list}
            items={rows}
            itemKey={(link) => link.id}
            estimateSize={80}
            itemProps={() => ({ className: styles.item })}
            pinnedKeys={management.managing ? [management.managing.id] : undefined}
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
                  {management.targetErrorPath === path && management.targetError ? (
                    <p className={styles.targetError} role="alert">
                      {management.targetError}
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
