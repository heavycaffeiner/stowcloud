import { lazy, Suspense } from 'react'
import type { AdminTab } from './hooks/use-admin-tab'
import { ErrorBoundary } from '../../../ui/ErrorBoundary'
import { ProgressCircular } from '../../../ui/ProgressCircular'

// Keep each admin section lazy so inactive tabs do not load their query graphs.
const UserManagementSection = lazy(async () => {
  const module = await import('../../../features/admin/UserManagementSection')
  return { default: module.UserManagementSection }
})
const GroupManagementSection = lazy(async () => {
  const module = await import('../../../features/admin/GroupManagementSection')
  return { default: module.GroupManagementSection }
})
const ShareManagementSection = lazy(async () => {
  const module = await import('../../../features/admin/ShareManagementSection')
  return { default: module.ShareManagementSection }
})
const StorageIndexSection = lazy(async () => {
  const module = await import('../../../features/admin/StorageIndexSection')
  return { default: module.StorageIndexSection }
})
const UploadSettingsSection = lazy(async () => {
  const module = await import('../../../features/admin/UploadSettingsSection')
  return { default: module.UploadSettingsSection }
})
const ServerSettingsSection = lazy(async () => {
  const module = await import('../../../features/admin/ServerSettingsSection')
  return { default: module.ServerSettingsSection }
})
const LogsSection = lazy(async () => {
  const module = await import('../../../features/admin/LogsSection')
  return { default: module.LogsSection }
})

export function SectionLoading({ label }: { label: string }) {
  return (
    <div className="sc-admin-loading" role="status" aria-live="polite">
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
    <div className="sc-admin-inner">
      <ErrorBoundary resetKey={tab}>
        <Suspense fallback={<SectionLoading label={loadingLabel} />}>
          {tab === 'users' ? (
            <>
              <section className="sc-admin-page-section sc-settings-card">
                <h2>{usersLabel}</h2>
                <UserManagementSection />
              </section>
              <section className="sc-admin-page-section sc-settings-card">
                <h2>{groupsLabel}</h2>
                <GroupManagementSection />
              </section>
            </>
          ) : null}
          {tab === 'shares' ? (
            <section className="sc-admin-page-section sc-settings-card">
              <ShareManagementSection />
            </section>
          ) : null}
          {tab === 'storage' ? (
            <section className="sc-admin-page-section">
              <StorageIndexSection />
              <UploadSettingsSection />
            </section>
          ) : null}
          {tab === 'server' ? (
            <section className="sc-admin-page-section">
              <ServerSettingsSection />
            </section>
          ) : null}
          {tab === 'logs' ? (
            <section className="sc-admin-page-section sc-settings-card">
              <LogsSection />
            </section>
          ) : null}
        </Suspense>
      </ErrorBoundary>
    </div>
  )
}
