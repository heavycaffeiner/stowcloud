import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { Component, lazy, Suspense, useEffect, useRef, useState } from 'react'
import { useI18n } from '../../lib/i18n/use-i18n'
import { sessionQuery } from '../../lib/query/session'
import { useDocumentTitle } from '../use-document-title'
import './admin.css'

// These literal imports remain lazy because each admin section owns its query graph;
// loading only the active tab keeps non-admins and inactive tabs off those graphs.
const UserManagementSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/UserManagementSection')
  return { default: module.UserManagementSection }
})
const GroupManagementSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/GroupManagementSection')
  return { default: module.GroupManagementSection }
})
const ShareManagementSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/ShareManagementSection')
  return { default: module.ShareManagementSection }
})
const StorageIndexSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/StorageIndexSection')
  return { default: module.StorageIndexSection }
})
const UploadSettingsSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/UploadSettingsSection')
  return { default: module.UploadSettingsSection }
})
const ServerSettingsSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/ServerSettingsSection')
  return { default: module.ServerSettingsSection }
})
const LogsSection = lazy(async () => {
  const module = await import('../../lib/ui/admin/LogsSection')
  return { default: module.LogsSection }
})

const TAB_VALUES = ['users', 'shares', 'storage', 'server', 'logs'] as const
type AdminTab = (typeof TAB_VALUES)[number]

function isAdminTab(value: string): value is AdminTab {
  return TAB_VALUES.includes(value as AdminTab)
}

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

function SectionLoading({ label }: { label: string }) {
  return <div className="sc-admin__loading" role="status" aria-live="polite"><mdui-circular-progress></mdui-circular-progress><span>{label}</span></div>
}

export function AdminPage() {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const [tab, setTab] = useState<AdminTab>(() => {
    const hash = location.hash.slice(1)
    return isAdminTab(hash) ? hash : 'users'
  })
  const seenHash = useRef(location.hash.slice(1))

  useDocumentTitle(t('admin.admin_stowcloud'))

  useEffect(() => {
    const reconcileHash = (): void => {
      const hash = location.hash.slice(1)
      if (isAdminTab(hash)) {
        seenHash.current = hash
        setTab(hash)
        return
      }
      seenHash.current = 'users'
      setTab('users')
      history.replaceState(history.state, '', `${location.pathname}${location.search}#users`)
    }

    if (!isAdminTab(seenHash.current)) {
      history.replaceState(history.state, '', `${location.pathname}${location.search}#users`)
      seenHash.current = 'users'
    }
    window.addEventListener('hashchange', reconcileHash)
    return () => window.removeEventListener('hashchange', reconcileHash)
  }, [])

  const selectTab = (next: AdminTab): void => {
    if (next === tab) return
    setTab(next)
    seenHash.current = next
    history.replaceState(history.state, '', `${location.pathname}${location.search}#${next}`)
  }

  if (session.isPending) {
    return <section className="sc-admin"><div className="sc-admin__title"><h1>{t('common.administrator')}</h1></div><div className="sc-admin__inner"><SectionLoading label={t('common.loading')} /></div></section>
  }
  if (session.isError || !session.data) {
    return <section className="sc-admin"><div className="sc-admin__title"><h1>{t('common.administrator')}</h1></div><div className="sc-admin__inner"><p className="sc-admin__error" role="alert">{t('common.could_not_load_list')}</p></div></section>
  }
  if (!session.data.user.is_admin) {
    return <section className="sc-admin"><div className="sc-admin__title"><h1>{t('common.administrator')}</h1></div><div className="sc-admin__inner"><p className="sc-admin__denied">{t('admin.only_administrators_can_see_screen')}</p></div></section>
  }

  return (
    <section className="sc-admin">
      <div className="sc-admin__title"><h1>{t('common.administrator')}</h1></div>
      <div className="sc-admin__head">
        <div className="sc-admin__head-inner" role="tablist" aria-label={t('admin.admin_sections')}>
          <div className="sc-admin__tabs">
            <mdui-button role="tab" aria-selected={tab === 'users'} variant={tab === 'users' ? 'tonal' : 'text'} onClick={() => selectTab('users')}>{t('admin.people_and_access')}</mdui-button>
            <mdui-button role="tab" aria-selected={tab === 'shares'} variant={tab === 'shares' ? 'tonal' : 'text'} onClick={() => selectTab('shares')}>{t('admin.shared_folders')}</mdui-button>
            <mdui-button role="tab" aria-selected={tab === 'storage'} variant={tab === 'storage' ? 'tonal' : 'text'} onClick={() => selectTab('storage')}>{t('admin.storage_and_transfers')}</mdui-button>
            <mdui-button role="tab" aria-selected={tab === 'server'} variant={tab === 'server' ? 'tonal' : 'text'} onClick={() => selectTab('server')}>{t('admin.server_and_security')}</mdui-button>
            <mdui-button role="tab" aria-selected={tab === 'logs'} variant={tab === 'logs' ? 'tonal' : 'text'} onClick={() => selectTab('logs')}>{t('common.logs')}</mdui-button>
          </div>
        </div>
      </div>
      <div className="sc-admin__inner">
        <SectionErrorBoundary key={tab} message={t('common.could_not_load_list')}>
          <Suspense fallback={<SectionLoading label={t('common.loading')} />}>
            {tab === 'users' ? <><section className="sc-admin__section"><h2>{t('admin.users')}</h2><UserManagementSection /></section><section className="sc-admin__section"><h2>{t('admin.groups')}</h2><GroupManagementSection /></section></> : null}
            {tab === 'shares' ? <section className="sc-admin__section"><ShareManagementSection /></section> : null}
            {tab === 'storage' ? <><section className="sc-admin__section"><StorageIndexSection /></section><section className="sc-admin__section"><UploadSettingsSection /></section></> : null}
            {tab === 'server' ? <section className="sc-admin__section"><ServerSettingsSection /></section> : null}
            {tab === 'logs' ? <section className="sc-admin__section"><LogsSection /></section> : null}
          </Suspense>
        </SectionErrorBoundary>
      </div>
    </section>
  )
}

