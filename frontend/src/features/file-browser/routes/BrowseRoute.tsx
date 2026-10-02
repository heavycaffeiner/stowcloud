import { useEffect, useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useI18n } from '../../../hooks/use-i18n'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { FilePreview } from '../../preview/components/FilePreview'
import { splatOf, splatPath } from '../browse-search'
import { lastFolder } from '../location'
import { useBrowseListing } from '../hooks/use-browse-listing'
import { useBrowseActions } from '../hooks/use-browse-actions'
import { useBrowsePageReset, useBrowseSearch, usePreviewParam } from '../hooks/use-browse-route-effects'
import { SelectionToolbar } from '../components/SelectionToolbar'
import { BrowseContent, BrowseNotice, BrowseOperation, BrowseToolbar } from '../components/BrowseView'
import * as styles from './BrowseRoute.css'

export function BrowseRoute() {
  const path = splatPath(useParams({ from: '/_app/b/$', select: (params) => params._splat }))
  return <BrowsePageContent key={path} path={path} />
}

function BrowsePageContent({ path }: { path: string }) {
  const { t } = useI18n()
  const { type = 'all', date = 'any' } = useBrowseSearch().search
  const [dragOver, setDragOver] = useState(false)
  useBrowsePageReset(path)
  useEffect(() => {
    lastFolder.value = path
  }, [path])
  useDocumentTitle(`${path.split('/').filter(Boolean).at(-1) ?? 'Stowcloud'} - Stowcloud`)
  const listing = useBrowseListing(path, { type, date })
  const actions = useBrowseActions(path, listing)
  const preview = usePreviewParam(listing.entries, listing.listing)
  const { session, selected, selectionBytes, canCreate } = listing

  if (path === '/' && session.data?.roots[0]) return <NavigateToRoot path={`/${session.data.roots[0].label}`} />
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
        <SelectionToolbar count={selected.length} bytes={selectionBytes} actions={actions.actionsFor(selected)} />
      ) : null}
      <BrowseContent path={path} listing={listing} actions={actions} dragOver={dragOver} />
      <BrowseOperation />
      <BrowseNotice />
      {actions.uploadInputs}
      <FilePreview
        entries={listing.entries}
        folder={path}
        entry={preview.entry}
        onShow={preview.show}
        onClose={preview.close}
        onDownload={(entry) => void actions.download([entry])}
        onEdit={(entry) => void actions.openEditor(entry)}
      />
    </div>
  )
}

/** The top level lists nothing of its own, so it opens the first root and keeps the URL's params. */
function NavigateToRoot({ path }: { path: string }) {
  const navigate = useNavigate()
  useEffect(() => {
    void navigate({ to: '/b/$', params: splatOf(path), search: true, replace: true })
  }, [navigate, path])
  return null
}
