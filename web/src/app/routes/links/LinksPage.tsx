import { useQuery } from '@tanstack/react-query'
import { describeApiError } from '../../../lib/api/error-text'
import { baseName, normalizePath } from '../../../lib/api/path-utils'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { adminLinksQuery } from '../../../lib/query/admin'
import { sessionQuery } from '../../../lib/query/session'
import { shareLinksQuery } from '../../../lib/query/shares'
import { VirtualList } from '../../../lib/ui/VirtualList'
import { useDocumentTitle } from '../../use-document-title'
import { SecondaryPageShell } from '../secondary/SecondaryPageShell'
import { SecondaryPageState } from '../secondary/SecondaryPageState'
import { LinkListRow } from './LinkRow'
import { useLinkManagement, type LinkRow } from './use-link-management'
import { ShareManageDialog } from '../../../features/shares/ShareManageDialog'
import '../simple-pages.css'

export function LinksPage() {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const isAdmin = session.data?.user.is_admin === true
  const own = useQuery({ ...shareLinksQuery(undefined), enabled: session.data !== undefined && !isAdmin })
  const all = useQuery({ ...adminLinksQuery(), enabled: isAdmin })
  const management = useLinkManagement(session.data?.user.id, t)
  const rows: LinkRow[] = isAdmin ? all.data ?? [] : own.data ?? []
  const activeQuery = isAdmin ? all : own
  const loading = session.isPending || activeQuery.isPending
  useDocumentTitle(t('links.title_stowcloud'))

  return (
    <SecondaryPageShell
      className="sc-links"
      title={t('nav.links')}
      refreshLabel={t('common.refresh')}
      onRefresh={() => void activeQuery.refetch()}
      overlay={management.managing ? <ShareManageDialog open path={normalizePath(management.managing.path)} targetName={baseName(management.managing.path) || management.managing.path} targetIsDir={management.managingTarget?.kind === 'dir'} onclose={() => management.setState({ managing: null, managingTarget: null })} /> : null}
    >
      <SecondaryPageState loading={loading} loadingLabel={t('common.loading')} error={activeQuery.error} errorText={describeApiError(activeQuery.error, t('links.could_not_load'))} empty={!loading && rows.length === 0} emptyText={t('links.empty')}>
        {rows.length > 0 ? <VirtualList className="sc-secondary-page__list sc-links__list" items={rows} itemKey={(link) => link.id} estimateSize={80} pinnedKeys={management.managing ? [management.managing.id] : undefined} renderItem={(link) => {
          const mine = management.isMine(link)
          const path = normalizePath(link.path)
          return <>
            <LinkListRow link={link} mine={mine} resolving={management.resolvingPath === path} targetSummary={management.targetSummary(link)} onOpen={() => void management.openManagement(link)} />
            {management.targetErrorPath === path && management.targetError ? <p className="sc-links__target-error" role="alert">{management.targetError}</p> : null}
          </>
        }} /> : null}
      </SecondaryPageState>
    </SecondaryPageShell>
  )
}