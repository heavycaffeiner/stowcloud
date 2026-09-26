import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { sessionQuery } from '../../../lib/query/session'
import { useDocumentTitle } from '../../use-document-title'
import { PageTabs, type PageTabItem } from '../PageTabs'
import { AdminPanels, SectionLoading } from './AdminPanels'
import { useAdminTab, type AdminTab } from './use-admin-tab'
import '../../../styles/app/routes/admin/admin.css.ts'

type AdminTabItem = PageTabItem<AdminTab>

function adminTabs(t: (key: string) => string): readonly AdminTabItem[] {
  return [
    { value: 'users', label: t('admin.people_and_access'), icon: 'admin' },
    { value: 'shares', label: t('admin.shared_folders'), icon: 'folder' },
    { value: 'storage', label: t('admin.storage_and_transfers'), icon: 'grid' },
    { value: 'server', label: t('admin.server_and_security'), icon: 'settings' },
    { value: 'logs', label: t('common.logs'), icon: 'recent' },
  ]
}

function AdminFrame({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  return <section className="sc-settings-page sc-admin"><header><h1>{t('common.administrator')}</h1></header>{children}</section>
}

export function AdminPage() {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const { tab, selectTab } = useAdminTab()

  useDocumentTitle(t('admin.admin_stowcloud'))

  if (session.isPending) {
    return <AdminFrame><div className="sc-admin-inner"><SectionLoading label={t('common.loading')} /></div></AdminFrame>
  }
  if (session.isError || !session.data) {
    return <AdminFrame><div className="sc-admin-inner"><p className="sc-admin-page-error" role="alert">{t('common.could_not_load_list')}</p></div></AdminFrame>
  }
  if (!session.data.user.is_admin) {
    return <AdminFrame><div className="sc-admin-inner"><p className="sc-admin-denied">{t('admin.only_administrators_can_see_screen')}</p></div></AdminFrame>
  }

  const tabs = adminTabs(t)
  return (
    <AdminFrame>
      <PageTabs label={t('admin.admin_sections')} items={tabs} active={tab} onSelect={selectTab} />
      <AdminPanels
        tab={tab}
        errorMessage={t('common.could_not_load_list')}
        loadingLabel={t('common.loading')}
        usersLabel={t('admin.users')}
        groupsLabel={t('admin.groups')}
      />
    </AdminFrame>
  )
}
