import { Icon } from '../../../lib/ui/Icon'
import { Button } from '../../../lib/ui/Button'
import { TextField } from '../../../lib/ui/TextField'
import { VirtualList } from '../../../lib/ui/VirtualList'
import { formatBytes } from '../../../lib/format/bytes'
import { useDocumentTitle } from '../../use-document-title'
import { usePublicShare } from './use-public-share'
import '../../../styles/app/routes/public-share.css.ts'

export function PublicSharePage() {
  const flow = usePublicShare()
  const { t, path, fileInput, state, setState, share, info, needsPassword, retryableError, crumbs, doneCount, unlock, openFolder, childPath, download, downloadFolder, pickFiles, retryUpload, removeFailedUpload } = flow
  const { password, unlockError, unlocking, queue, uploading, pathGone } = state
  const title = info?.label ?? info?.name ?? t('common.share_links')
  useDocumentTitle(`${title} - Stowcloud`)
  let loadError: string | null = null
  if (share.error?.constructor?.name === 'ShareNotFoundError') loadError = t('public_share.link_has_expired_or_does')
  else if (share.error && !needsPassword && !pathGone) loadError = t('public_share.could_not_load')
  return (
    <main className="sc-public-share">
      <header className="sc-public-share-header"><strong>Stowcloud</strong>{t('public_share.public_share_link')}</header>
      {share.isPending ? <p className="sc-public-share-status">{t('common.loading')}</p> : null}
      {loadError ? <section role="alert" className="sc-public-share-state sc-public-share-state--error"><p>{loadError}</p>{retryableError ? <Button variant="outlined" loading={share.isFetching} onClick={() => void share.refetch()}>{t('public_share.retry')}</Button> : null}</section> : null}
      {needsPassword ? <form className="sc-public-share-unlock" onSubmit={unlock}><h1>{t('public_share.link_password_protected')}</h1><TextField value={password} label={t('common.password')} type="password" autoFocus autoComplete="off" error={unlockError} onValueChange={(password) => setState({ password })} /><div className="sc-public-share-unlock-actions"><Button type="submit" disabled={!password} loading={unlocking}>{t('public_share.unlock')}</Button></div></form> : null}
      {info ? <section>
        <h1 className="sc-public-share-title">{info.label || info.name}</h1>
        {crumbs.length > 0 ? <nav className="sc-public-share-crumbs" aria-label={t('public_share.location')}><ol><li><button type="button" className="sc-public-share-crumb sc-focus-ring" onClick={() => openFolder('')}>{t('public_share.top_folder')}</button></li>{crumbs.map((crumb, index) => <li key={crumb.path}><span className="sc-public-share-crumb-sep" aria-hidden="true">/</span>{index === crumbs.length - 1 ? <span aria-current="page">{crumb.name}</span> : <button type="button" className="sc-public-share-crumb sc-focus-ring" onClick={() => openFolder(crumb.path)}>{crumb.name}</button>}</li>)}</ol></nav> : null}
        {pathGone ? <p className="sc-public-share-status sc-public-share-status--error">{t('public_share.folder_gone')}</p> : null}
        {info.isDrop ? <><p className="sc-public-share-status">{t('public_share.link_upload_only_nothing_can')}</p><div className="sc-public-share-drop"><input ref={fileInput} className="sc-public-share-file" type="file" multiple onChange={pickFiles} /><Button disabled={uploading} onClick={() => fileInput.current?.click()}>{t('share_drop.pick_files')}</Button>{info.maxUploadBytes !== null ? <p className="sc-public-share-status">{t('share_drop.limit_hint', { size: formatBytes(info.maxUploadBytes) })}</p> : null}</div>{queue.length > 0 ? <><p className="sc-public-share-status">{t('share_drop.uploading', { done: doneCount, total: queue.length })}</p><VirtualList className="sc-public-share-list" items={queue} itemKey={(item) => item.id} estimateSize={56} itemProps={() => ({ className: 'sc-public-share-row' })} renderItem={(item, index) => <><span className="sc-filename sc-public-share-name">{item.file.name}</span><span className={`sc-public-share-size${item.status === 'error' ? ' sc-public-share-status--error' : ''}`}>{item.status === 'done' ? t('share_drop.uploaded_as', { name: item.storedAs }) : item.failure === 'too_large' ? t('share_drop.too_large') : item.failure === 'failed' ? t('share_drop.failed') : item.status === 'uploading' ? t('share_drop.in_progress') : formatBytes(item.file.size)}</span>{item.status === 'error' ? <div className="sc-public-share-row-actions"><Button variant="text" onClick={() => retryUpload(index)}>{t('share_drop.retry')}</Button><Button variant="text" onClick={() => removeFailedUpload(index)}>{t('share_drop.remove')}</Button></div> : null}</>} /></> : null}</> : !info.isDir ? <><p className="sc-public-share-status">{formatBytes(info.size)}</p>{info.canDownload ? <Button onClick={() => download(path)}>{t('common.download')}</Button> : null}</> : <>{(info.entries ?? []).length > 0 ? <VirtualList key={path} className="sc-public-share-list" items={info.entries ?? []} itemKey={(entry) => entry.name} estimateSize={56} itemProps={() => ({ className: 'sc-public-share-row' })} renderItem={(entry) => <><span className="sc-public-share-icon" aria-hidden="true"><Icon name={entry.kind === 'dir' ? 'folder' : 'draft'} size={20} /></span>{entry.kind === 'dir' ? <button type="button" className="sc-filename sc-public-share-name sc-public-share-folder sc-focus-ring" aria-label={t('public_share.open_folder', { name: entry.name })} onClick={() => openFolder(childPath(entry.name))}>{entry.name}</button> : <span className="sc-filename sc-public-share-name">{entry.name}</span>}<span className="sc-public-share-size">{entry.kind === 'dir' ? '-' : formatBytes(entry.size)}</span><span className="sc-public-share-action">{entry.kind === 'file' && info.canDownload ? <Button variant="text" onClick={() => download(childPath(entry.name))}>{t('common.download')}</Button> : null}</span></>} /> : <ul className="sc-public-share-list"><li className="sc-public-share-row sc-public-share-row--empty">{t('public_share.empty')}</li></ul>}{info.canDownload ? <Button onClick={downloadFolder}>{t('public_share.download_folder')}</Button> : null}</>}
      </section> : null}
    </main>
  )
}
