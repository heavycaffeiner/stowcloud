import { useEffect, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { useI18n } from '../../../hooks/use-i18n'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { normalizePath } from '../../../lib/path-utils'
import { lastFolder } from '../location'
import { useBrowseListing } from '../hooks/use-browse-listing'
import { useBrowseActions } from '../hooks/use-browse-actions'
import { useBrowsePageReset } from '../hooks/use-browse-route-effects'
import { BrowseSelectionBar } from './BrowseSelectionBar'
import { BrowseContent, BrowseNotice, BrowseOperation, BrowseToolbar } from './BrowseView'
import * as styles from './BrowseRoute.css'

export function BrowseRoute() {
  const params = useParams()
  const path = normalizePath(`/${params['*'] ?? ''}`)
  return <BrowsePageContent key={path} path={path} />
}

function BrowsePageContent({ path }: { path: string }) {
  const { t } = useI18n()
  const { search } = useLocation()
  const [dragOver, setDragOver] = useState(false)
  useBrowsePageReset(path)
  useEffect(() => {
    lastFolder.value = path
  }, [path])
  useDocumentTitle(`${path.split('/').filter(Boolean).at(-1) ?? 'Stowcloud'} - Stowcloud`)
  const listing = useBrowseListing(path)
  const actions = useBrowseActions(path, listing)
  const { session, selected, selectionBytes, canCreate } = listing

  if (path === '/' && session.data?.roots[0])
    return <NavigateToRoot path={session.data.roots[0].label} search={search} />
  return (
    <div
      className={styles.root}
      role="region"
      aria-label={t('browse.file_browser')}
      onDragOver={(event) => {
        event.preventDefault()
        setDragOver(canCreate)
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(event) => {
        setDragOver(false)
        void actions.drop(event)
      }}
    >
      <BrowseToolbar path={path} listing={listing} actions={actions} />
      {selected.length ? (
        <BrowseSelectionBar count={selected.length} bytes={selectionBytes} actions={actions.actionsFor(selected)} />
      ) : null}
      <BrowseContent path={path} listing={listing} actions={actions} dragOver={dragOver} />
      <BrowseOperation />
      <BrowseNotice />
      {actions.uploadInputs}
    </div>
  )
}

function NavigateToRoot({ path, search }: { path: string; search: string }) {
  const navigate = useNavigate()
  useEffect(() => {
    void navigate(`/b/${encodeURIComponent(path)}${search}`, { replace: true })
  }, [navigate, path, search])
  return null
}
