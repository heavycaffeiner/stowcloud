import { lazy, Suspense } from 'react'
import type { AdminTab } from '../hooks/use-admin-tab'
import { ErrorBoundary } from '../../../ui/ErrorBoundary'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import * as styles from './AdminPanels.css'
import * as adminPageStyles from './AdminPage.css'
import * as settingsCardStyles from '../../settings/SettingsCard.css'
import * as textFieldStyles from '../../../ui/TextField.css'
import { cx } from '../../../ui/cx'

// Keep each admin section lazy so inactive tabs do not load their query graphs.
const UserManagementSection = lazy(async () => {
  const module = await import('../UserManagementSection')
  return { default: module.UserManagementSection }
})
const GroupManagementSection = lazy(async () => {
  const module = await import('../GroupManagementSection')
  return { default: module.GroupManagementSection }
})
const ShareManagementSection = lazy(async () => {
  const module = await import('../ShareManagementSection')
  return { default: module.ShareManagementSection }
})
const StorageIndexSection = lazy(async () => {
  const module = await import('../StorageIndexSection')
  return { default: module.StorageIndexSection }
})
const UploadSettingsSection = lazy(async () => {
  const module = await import('../UploadSettingsSection')
  return { default: module.UploadSettingsSection }
})
const ServerSettingsSection = lazy(async () => {
  const module = await import('../ServerSettingsSection')
  return { default: module.ServerSettingsSection }
})
const LogsSection = lazy(async () => {
  const module = await import('../LogsSection')
  return { default: module.LogsSection }
})

export function SectionLoading({ label }: { label: string }) {
  return (
    <div className={styles.loading} role="status" aria-live="polite">
      <ProgressCircular size={40} />
      <span>{label}</span>
    </div>
  )
}

interface AdminPanelsProps {
  tab: AdminTab
  loadingLabel: string
  usersLabel: string
  groupsLabel: string
}

export function AdminPanels({ tab, loadingLabel, usersLabel, groupsLabel }: AdminPanelsProps) {
  return (
    <div className={adminPageStyles.inner}>
      <ErrorBoundary resetKey={tab}>
        <Suspense fallback={<SectionLoading label={loadingLabel} />}>
          {tab === 'users' ? (
            <>
              <section className={cx(styles.pageSection, settingsCardStyles.card, textFieldStyles.onLowSurface)}>
                <h2 className={styles.pageTitle}>{usersLabel}</h2>
                <UserManagementSection />
              </section>
              <section className={cx(styles.pageSection, settingsCardStyles.card, textFieldStyles.onLowSurface)}>
                <h2 className={styles.pageTitle}>{groupsLabel}</h2>
                <GroupManagementSection />
              </section>
            </>
          ) : null}
          {tab === 'shares' ? (
            <section className={cx(styles.pageSection, settingsCardStyles.card, textFieldStyles.onLowSurface)}>
              <ShareManagementSection />
            </section>
          ) : null}
          {tab === 'storage' ? (
            <section className={styles.pageSection}>
              <StorageIndexSection />
              <UploadSettingsSection />
            </section>
          ) : null}
          {tab === 'server' ? (
            <section className={styles.pageSection}>
              <ServerSettingsSection />
            </section>
          ) : null}
          {tab === 'logs' ? (
            <section className={cx(styles.pageSection, settingsCardStyles.card, textFieldStyles.onLowSurface)}>
              <LogsSection />
            </section>
          ) : null}
        </Suspense>
      </ErrorBoundary>
    </div>
  )
}
