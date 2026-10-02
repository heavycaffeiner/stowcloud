import { useEffect, useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useForm, useWatch } from 'react-hook-form'
import { useLocation, useNavigate, useParams, useSearch, type HistoryState } from '@tanstack/react-router'
import { cx, Icon, StowButton, StowFormTextField, VirtualList } from '@/shared/ui'
import { formatBytes } from '../../../lib/format/bytes'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { useI18n } from '../../../hooks/use-i18n'
import {
  ShareNotFoundError,
  SharePasswordRequiredError,
  SharePathGoneError,
  shareDownloadUrl,
  shareZipUrl,
  unlockShare,
  usePublicShare,
  type ShareInfo
} from '../api'
import { createPublicShareQueue, type DropItem } from '../logic/public-share-queue'
import * as styles from './PublicSharePage.css'
import { focusRing } from '@/shared/theme'

const childPath = (path: string, name: string): string => (path ? `${path}/${name}` : name)

// Downloads are plain navigations, so the server's download cap counts only real ones.
const navigateTo = (url: string): void => {
  window.location.href = url
}

export function PublicSharePage() {
  const { t } = useI18n()
  const token = useParams({ from: '/s/$token', select: (params) => params.token })
  const path = useSearch({ from: '/s/$token', select: (search) => search.path ?? '' })
  const navigate = useNavigate()
  const share = usePublicShare(token, path)
  const { data: info, error } = share
  const needsPassword = error instanceof SharePasswordRequiredError
  // A folder that vanished sends the visitor back to the top, which then says why.
  const pathGone = useLocation({ select: (location) => (location.state as { pathGone?: unknown }).pathGone === true })
  const loadError =
    error instanceof ShareNotFoundError
      ? t('public_share.link_has_expired_or_does')
      : error && !needsPassword && !(error instanceof SharePathGoneError)
        ? t('public_share.could_not_load')
        : null
  useDocumentTitle(`${info?.label ?? info?.name ?? t('common.share_links')} - Stowcloud`)

  useEffect(() => {
    if (error instanceof SharePathGoneError)
      void navigate({ to: '/s/$token', params: { token }, replace: true, state: { pathGone: true } as HistoryState })
  }, [error, navigate, token])

  const openFolder = (next: string): void =>
    void navigate({ to: '/s/$token', params: { token }, search: { path: next || undefined } })

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
          {error instanceof ShareNotFoundError ? null : (
            <StowButton
              className={styles.stateAction}
              variant="outlined"
              loading={share.isFetching}
              onClick={() => void share.refetch()}
            >
              {t('public_share.retry')}
            </StowButton>
          )}
        </section>
      ) : null}
      {needsPassword ? <UnlockForm token={token} onUnlocked={share.refetch} /> : null}
      {info ? (
        <section>
          <h1 className={styles.title}>{info.label || info.name}</h1>
          {path ? <Breadcrumbs path={path} onOpen={openFolder} /> : null}
          {pathGone ? <p className={cx(styles.status, styles.statusError)}>{t('public_share.folder_gone')}</p> : null}
          {info.isDrop ? (
            <DropZone key={token} token={token} maxUploadBytes={info.maxUploadBytes} />
          ) : !info.isDir ? (
            <>
              <p className={styles.status}>{formatBytes(info.size)}</p>
              {info.canDownload ? (
                <StowButton onClick={() => navigateTo(shareDownloadUrl(token, path))}>
                  {t('common.download')}
                </StowButton>
              ) : null}
            </>
          ) : (
            <FolderListing token={token} path={path} info={info} onOpen={openFolder} />
          )}
        </section>
      ) : null}
    </main>
  )
}

function UnlockForm({ token, onUnlocked }: { token: string; onUnlocked: () => Promise<unknown> }) {
  const { t } = useI18n()
  const { control, handleSubmit, setError } = useForm({ defaultValues: { password: '' } })
  const password = useWatch({ control, name: 'password' })
  // Pending until the share has been read again with the unlock cookie.
  const unlock = useMutation({
    mutationFn: async (password: string) => {
      if (!(await unlockShare(token, password))) return false
      await onUnlocked()
      return true
    }
  })
  const submit = handleSubmit(({ password }) => {
    if (unlock.isPending || !password) return
    unlock.mutate(password, {
      onSuccess: (ok) => {
        if (!ok) setError('password', { message: t('common.incorrect_password') })
      },
      onError: () => setError('password', { message: t('public_share.could_not_verify_try_again') })
    })
  })
  return (
    <form className={styles.unlock} onSubmit={(event) => void submit(event)}>
      <h1 className={styles.unlockTitle}>{t('public_share.link_password_protected')}</h1>
      <StowFormTextField
        control={control}
        name="password"
        label={t('common.password')}
        type="password"
        autoFocus
        autoComplete="off"
      />
      <div className={styles.unlockActions}>
        <StowButton type="submit" disabled={!password} loading={unlock.isPending}>
          {t('public_share.unlock')}
        </StowButton>
      </div>
    </form>
  )
}

function Breadcrumbs({ path, onOpen }: { path: string; onOpen: (path: string) => void }) {
  const { t } = useI18n()
  const crumbs = path
    .split('/')
    .filter(Boolean)
    .map((name, index, parts) => ({ name, path: parts.slice(0, index + 1).join('/') }))
  return (
    <nav className={styles.crumbs} aria-label={t('public_share.location')}>
      <ol className={styles.crumbList}>
        <li className={styles.crumbItem}>
          <button type="button" className={cx(styles.crumb, focusRing)} onClick={() => onOpen('')}>
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
              <button type="button" className={cx(styles.crumb, focusRing)} onClick={() => onOpen(crumb.path)}>
                {crumb.name}
              </button>
            )}
          </li>
        ))}
      </ol>
    </nav>
  )
}

function DropZone({ token, maxUploadBytes }: { token: string; maxUploadBytes: number | null }) {
  const { t } = useI18n()
  const fileInput = useRef<HTMLInputElement>(null)
  const [state, setState] = useState<{ queue: DropItem[]; uploading: boolean }>({ queue: [], uploading: false })
  // React drops the updates of an unmounted zone, while its uploads still finish.
  const [queue] = useState(() =>
    createPublicShareQueue({ token, setState: (next) => setState((current) => ({ ...current, ...next })) })
  )
  const doneCount = state.queue.filter((item) => item.status === 'done').length
  const pickFiles = (event: React.ChangeEvent<HTMLInputElement>): void => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ''
    if (files.length) queue.add(files, maxUploadBytes)
  }
  return (
    <>
      <p className={styles.status}>{t('public_share.link_upload_only_nothing_can')}</p>
      <div className={styles.drop}>
        <input ref={fileInput} className={styles.file} type="file" multiple onChange={pickFiles} />
        <StowButton disabled={state.uploading} onClick={() => fileInput.current?.click()}>
          {t('share_drop.pick_files')}
        </StowButton>
        {maxUploadBytes !== null ? (
          <p className={styles.status}>{t('share_drop.limit_hint', { size: formatBytes(maxUploadBytes) })}</p>
        ) : null}
      </div>
      {state.queue.length > 0 ? (
        <>
          <p className={styles.status}>{t('share_drop.uploading', { done: doneCount, total: state.queue.length })}</p>
          <VirtualList
            className={styles.list}
            items={state.queue}
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
                    <StowButton className={styles.rowAction} variant="text" onClick={() => queue.retry(index)}>
                      {t('share_drop.retry')}
                    </StowButton>
                    <StowButton className={styles.rowAction} variant="text" onClick={() => queue.removeFailed(index)}>
                      {t('share_drop.remove')}
                    </StowButton>
                  </div>
                ) : null}
              </>
            )}
          />
        </>
      ) : null}
    </>
  )
}

interface FolderListingProps {
  token: string
  path: string
  info: ShareInfo
  onOpen: (path: string) => void
}

function FolderListing({ token, path, info, onOpen }: FolderListingProps) {
  const { t } = useI18n()
  const entries = info.entries ?? []
  return (
    <>
      {entries.length > 0 ? (
        <VirtualList
          key={path}
          className={styles.list}
          items={entries}
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
                  className={cx(styles.name, styles.folder, focusRing)}
                  aria-label={t('public_share.open_folder', { name: entry.name })}
                  onClick={() => onOpen(childPath(path, entry.name))}
                >
                  {entry.name}
                </button>
              ) : (
                <span className={styles.name}>{entry.name}</span>
              )}
              <span className={styles.size}>{entry.kind === 'dir' ? '-' : formatBytes(entry.size)}</span>
              <span className={styles.action}>
                {entry.kind === 'file' && info.canDownload ? (
                  <StowButton
                    variant="text"
                    onClick={() => navigateTo(shareDownloadUrl(token, childPath(path, entry.name)))}
                  >
                    {t('common.download')}
                  </StowButton>
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
      {info.canDownload ? (
        <StowButton onClick={() => navigateTo(shareZipUrl(token, path))}>
          {t('public_share.download_folder')}
        </StowButton>
      ) : null}
    </>
  )
}
