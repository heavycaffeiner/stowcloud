import type { ReactNode } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { t } from '../../../i18n'
import { useSession } from '../../auth/api'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { type StowPageTab, StowPageTabs, StowTabbedPage } from '@/shared/ui'
import { AdminPanels, SectionLoading } from '../components/AdminPanels'
import { useAdminTab } from '../hooks/use-admin-tab'
import type { AdminTab } from '../tabs'
import * as styles from './AdminPage.css'

type AdminTabItem = StowPageTab<AdminTab>

function adminTabs(): readonly AdminTabItem[] {
  return [
    { value: 'users', label: t('admin.people_and_access'), icon: 'admin' },
    { value: 'shares', label: t('admin.shared_folders'), icon: 'folder' },
    { value: 'storage', label: t('admin.storage_and_transfers'), icon: 'grid' },
    { value: 'server', label: t('admin.server_and_security'), icon: 'settings' },
    { value: 'logs', label: t('common.logs'), icon: 'recent' }
  ]
}

function AdminFrame({ children }: { children: ReactNode }) {
  const { t } = useI18n()
  return <StowTabbedPage title={t('common.administrator')}>{children}</StowTabbedPage>
}

export function AdminPage() {
  const { t } = useI18n()
  const session = useSession()
  const { tab, selectTab } = useAdminTab()

  useDocumentTitle(t('admin.admin_stowcloud'))

  if (session.isPending) {
    return (
      <AdminFrame>
        <div className={styles.inner}>
          <SectionLoading label={t('common.loading')} />
        </div>
      </AdminFrame>
    )
  }
  if (session.isError || !session.data) {
    return (
      <AdminFrame>
        <div className={styles.inner}>
          <p className={styles.pageError} role="alert">
            {t('common.could_not_load_list')}
          </p>
        </div>
      </AdminFrame>
    )
  }
  if (!session.data.user.is_admin) {
    return (
      <AdminFrame>
        <div className={styles.inner}>
          <p className={styles.denied}>{t('admin.only_administrators_can_see_screen')}</p>
        </div>
      </AdminFrame>
    )
  }

  return (
    <AdminFrame>
      <StowPageTabs label={t('admin.admin_sections')} items={adminTabs()} active={tab} onSelect={selectTab} />
      <AdminPanels tab={tab} />
    </AdminFrame>
  )
}
