import { Icon } from '../../../ui/Icon'
import { Button } from '../../../ui/Button'
import { TextField } from '../../../ui/TextField'
import { VirtualList } from '../../../ui/VirtualList'
import { formatBytes } from '../../../lib/format/bytes'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { usePublicShare } from '../hooks/use-public-share'
import * as styles from './PublicSharePage.css'
import * as utilitiesStyles from '../../../ui/utilities.css'
import { cx } from '../../../ui/cx'

export function PublicSharePage() {
  const flow = usePublicShare()
  const {
    t,
    path,
    fileInput,
    state,
    setState,
    share,
    info,
    needsPassword,
    retryableError,
    crumbs,
    doneCount,
    unlock,
    openFolder,
    childPath,
    download,
    downloadFolder,
    pickFiles,
    retryUpload,
    removeFailedUpload
  } = flow
  const { password, unlockError, unlocking, queue, uploading, pathGone } = state
  const title = info?.label ?? info?.name ?? t('common.share_links')
  useDocumentTitle(`${title} - Stowcloud`)
  let loadError: string | null = null
  if (share.error?.constructor?.name === 'ShareNotFoundError') loadError = t('public_share.link_has_expired_or_does')
  else if (share.error && !needsPassword && !pathGone) loadError = t('public_share.could_not_load')
  return (
    <main className={styles.root}>
      <header className={styles.header}>
        <strong>Stowcloud</strong>
        {t('public_share.public_share_link')}
      </header>
      {share.isPending ? <p className={styles.status}>{t('common.loading')}</p> : null}
      {loadError ? (
        <section role="alert" className={cx(styles.state, styles.stateError)}>
          <p className={styles.stateText}>{loadError}</p>
          {retryableError ? (
            <Button
              className={styles.stateAction}
              variant="outlined"
              loading={share.isFetching}
              onClick={() => void share.refetch()}
            >
              {t('public_share.retry')}
            </Button>
          ) : null}
        </section>
      ) : null}
      {needsPassword ? (
        <form className={styles.unlock} onSubmit={unlock}>
          <h1 className={styles.unlockTitle}>{t('public_share.link_password_protected')}</h1>
          <TextField
            value={password}
            label={t('common.password')}
            type="password"
            autoFocus
            autoComplete="off"
            error={unlockError}
            onValueChange={(password) => setState({ password })}
          />
          <div className={styles.unlockActions}>
            <Button type="submit" disabled={!password} loading={unlocking}>
              {t('public_share.unlock')}
            </Button>
          </div>
        </form>
      ) : null}
      {info ? (
        <section>
          <h1 className={styles.title}>{info.label || info.name}</h1>
          {crumbs.length > 0 ? (
            <nav className={styles.crumbs} aria-label={t('public_share.location')}>
              <ol className={styles.crumbList}>
                <li className={styles.crumbItem}>
                  <button
                    type="button"
                    className={cx(styles.crumb, utilitiesStyles.focusRing)}
                    onClick={() => openFolder('')}
                  >
                    {t('public_share.top_folder')}
                  </button>
                </li>
                {crumbs.map((crumb, index) => (
                  <li key={crumb.path} className={styles.crumbItem}>
                    <span className={styles.crumbSep} aria-hidden="true">
                      /
                    </span>
                    {index === crumbs.length - 1 ? (
                      <span className={styles.crumbCurrent} aria-current="page">
                        {crumb.name}
                      </span>
                    ) : (
                      <button
                        type="button"
                        className={cx(styles.crumb, utilitiesStyles.focusRing)}
                        onClick={() => openFolder(crumb.path)}
                      >
                        {crumb.name}
                      </button>
                    )}
                  </li>
                ))}
              </ol>
            </nav>
          ) : null}
          {pathGone ? <p className={cx(styles.status, styles.statusError)}>{t('public_share.folder_gone')}</p> : null}
          {info.isDrop ? (
            <>
              <p className={styles.status}>{t('public_share.link_upload_only_nothing_can')}</p>
              <div className={styles.drop}>
                <input ref={fileInput} className={styles.file} type="file" multiple onChange={pickFiles} />
                <Button disabled={uploading} onClick={() => fileInput.current?.click()}>
                  {t('share_drop.pick_files')}
                </Button>
                {info.maxUploadBytes !== null ? (
                  <p className={styles.status}>
                    {t('share_drop.limit_hint', { size: formatBytes(info.maxUploadBytes) })}
                  </p>
                ) : null}
              </div>
              {queue.length > 0 ? (
                <>
                  <p className={styles.status}>{t('share_drop.uploading', { done: doneCount, total: queue.length })}</p>
                  <VirtualList
                    className={styles.list}
                    items={queue}
                    itemKey={(item) => item.id}
                    estimateSize={56}
                    itemProps={() => ({ className: styles.row })}
                    renderItem={(item, index) => (
                      <>
                        <span className={styles.name}>{item.file.name}</span>
                        <span className={cx(styles.size, item.status === 'error' && styles.statusError)}>
                          {item.status === 'done'
                            ? t('share_drop.uploaded_as', { name: item.storedAs })
                            : item.failure === 'too_large'
                              ? t('share_drop.too_large')
                              : item.failure === 'failed'
                                ? t('share_drop.failed')
                                : item.status === 'uploading'
                                  ? t('share_drop.in_progress')
                                  : formatBytes(item.file.size)}
                        </span>
                        {item.status === 'error' ? (
                          <div className={styles.rowActions}>
                            <Button className={styles.rowAction} variant="text" onClick={() => retryUpload(index)}>
                              {t('share_drop.retry')}
                            </Button>
                            <Button
                              className={styles.rowAction}
                              variant="text"
                              onClick={() => removeFailedUpload(index)}
                            >
                              {t('share_drop.remove')}
                            </Button>
                          </div>
                        ) : null}
                      </>
                    )}
                  />
                </>
              ) : null}
            </>
          ) : !info.isDir ? (
            <>
              <p className={styles.status}>{formatBytes(info.size)}</p>
              {info.canDownload ? <Button onClick={() => download(path)}>{t('common.download')}</Button> : null}
            </>
          ) : (
            <>
              {(info.entries ?? []).length > 0 ? (
                <VirtualList
                  key={path}
                  className={styles.list}
                  items={info.entries ?? []}
                  itemKey={(entry) => entry.name}
                  estimateSize={56}
                  itemProps={() => ({ className: styles.row })}
                  renderItem={(entry) => (
                    <>
                      <span className={styles.icon} aria-hidden="true">
                        <Icon name={entry.kind === 'dir' ? 'folder' : 'draft'} size={20} />
                      </span>
                      {entry.kind === 'dir' ? (
                        <button
                          type="button"
                          className={cx(styles.name, styles.folder, utilitiesStyles.focusRing)}
                          aria-label={t('public_share.open_folder', { name: entry.name })}
                          onClick={() => openFolder(childPath(entry.name))}
                        >
                          {entry.name}
                        </button>
                      ) : (
                        <span className={styles.name}>{entry.name}</span>
                      )}
                      <span className={styles.size}>{entry.kind === 'dir' ? '-' : formatBytes(entry.size)}</span>
                      <span className={styles.action}>
                        {entry.kind === 'file' && info.canDownload ? (
                          <Button variant="text" onClick={() => download(childPath(entry.name))}>
                            {t('common.download')}
                          </Button>
                        ) : null}
                      </span>
                    </>
                  )}
                />
              ) : (
                <ul className={styles.list}>
                  <li className={cx(styles.row, styles.rowEmpty)}>{t('public_share.empty')}</li>
                </ul>
              )}
              {info.canDownload ? <Button onClick={downloadFolder}>{t('public_share.download_folder')}</Button> : null}
            </>
          )}
        </section>
      ) : null}
    </main>
  )
}
