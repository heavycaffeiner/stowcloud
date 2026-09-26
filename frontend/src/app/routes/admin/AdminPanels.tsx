import type { ReactNode } from 'react'
import { Component, lazy, Suspense } from 'react'
import type { AdminTab } from './use-admin-tab'

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

class SectionErrorBoundary extends Component<{ children: ReactNode; message: string }, { error: Error | null }> {
  state = { error: null as Error | null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  render() {
    if (this.state.error) return <p className="sc-admin__error" role="alert">{this.props.message}</p>
    return this.props.children
  }
}

export function SectionLoading({ label }: { label: string }) {
  return <div className="sc-admin__loading" role="status" aria-live="polite"><mdui-circular-progress></mdui-circular-progress><span>{label}</span></div>
}

interface AdminPanelsProps {
  tab: AdminTab
  errorMessage: string
  loadingLabel: string
  usersLabel: string
  groupsLabel: string
}

export function AdminPanels({ tab, errorMessage, loadingLabel, usersLabel, groupsLabel }: AdminPanelsProps) {
  return (
    <div className="sc-admin__inner">
      <SectionErrorBoundary key={tab} message={errorMessage}>
        <Suspense fallback={<SectionLoading label={loadingLabel} />}>
          {tab === 'users' ? <><section className="sc-admin__section sc-settings-card"><h2>{usersLabel}</h2><UserManagementSection /></section><section className="sc-admin__section sc-settings-card"><h2>{groupsLabel}</h2><GroupManagementSection /></section></> : null}
          {tab === 'shares' ? <section className="sc-admin__section sc-settings-card"><ShareManagementSection /></section> : null}
          {tab === 'storage' ? <section className="sc-admin__section"><StorageIndexSection /><UploadSettingsSection /></section> : null}
          {tab === 'server' ? <section className="sc-admin__section"><ServerSettingsSection /></section> : null}
          {tab === 'logs' ? <section className="sc-admin__section sc-settings-card"><LogsSection /></section> : null}
        </Suspense>
      </SectionErrorBoundary>
    </div>
  )
}
