import { useEffect, useRef } from 'react'
import { useI18n } from '../i18n/use-i18n'
import { formatBytes, formatEta, formatRate } from '../format/bytes'
import { pauseUpload, resumeUpload, cancelUpload } from '../upload/queue'
import { uploads, type UploadItem } from '../store/upload.store'
import { useStore } from '../store/use-store'
import { Icon } from './Icon'
import { IconButton } from './IconButton'
import { VirtualList } from './VirtualList'

export function UploadTray() {
  const { t } = useI18n()
  const items = useStore(uploads, (state) => state.items)
  const open = useStore(uploads, (state) => state.open)
  const previous = useRef(new Map<string, UploadItem['status']>())
  const pendingStarts = useRef(new Set<string>())
  const startTimer = useRef<number | null>(null)
  const politeRef = useRef<HTMLDivElement | null>(null)
  const assertiveRef = useRef<HTMLDivElement | null>(null)

  const announce = (target: 'polite' | 'assertive', text: string): void => {
    const element = target === 'polite' ? politeRef.current : assertiveRef.current
    if (!element) return
    element.textContent = ''
    window.setTimeout(() => {
      element.textContent = text
    }, 0)
  }

  useEffect(() => {
    const flushStarts = (): void => {
      startTimer.current = null
      const names = [...pendingStarts.current]
        .map((id) => items.find((item) => item.id === id))
        .filter((item): item is UploadItem => !!item && (item.status === 'uploading' || item.status === 'paused'))
        .map((item) => item.name)
      pendingStarts.current.clear()
      if (names.length === 0) return
      announce('polite', names.length === 1 ? t('upload.uploading', { name: names[0] }) : t('upload.uploading_files', { count: names.length }))
    }

    const seen = new Set<string>()
    for (const item of items) {
      seen.add(item.id)
      const prior = previous.current.get(item.id)
      if (prior === undefined) {
        pendingStarts.current.add(item.id)
        if (startTimer.current === null) startTimer.current = window.setTimeout(flushStarts, 150)
      } else if (prior !== 'done' && item.status === 'done') {
        announce('polite', t('upload.finished_uploading', { name: item.name }))
      } else if (prior !== 'error' && item.status === 'error') {
        announce('assertive', `${t('upload.failed_upload', { name: item.name })} ${item.message ? t(item.message, item.messageParams) : ''}`.trim())
      }
      previous.current.set(item.id, item.status)
    }
    for (const id of previous.current.keys()) if (!seen.has(id)) previous.current.delete(id)

    return () => {
      if (startTimer.current !== null) {
        window.clearTimeout(startTimer.current)
        startTimer.current = null
      }
    }
  }, [items, t])

  const activeCount = items.filter((item) => item.status === 'uploading' || item.status === 'paused').length
  const failedCount = items.filter((item) => item.status === 'error').length

  return (
    <>
      <div ref={politeRef} className="sc-upload-tray__sr-only" role="status" aria-live="polite" aria-atomic="true"></div>
      <div ref={assertiveRef} className="sc-upload-tray__sr-only" role="alert" aria-live="assertive" aria-atomic="true"></div>
      {items.length > 0 ? (
        <section className={open ? 'sc-upload-tray' : 'sc-upload-tray sc-upload-tray--collapsed'} aria-label={t('common.upload')}>
          <header className="sc-upload-tray__header">
            <button className="sc-upload-tray__title" type="button" onClick={() => uploads.setOpen(!open)} aria-expanded={open}>
              <Icon name="upload_file" />
              <span>{t('common.upload')}</span>
              <span>{activeCount > 0 ? `(${activeCount})` : failedCount > 0 ? t('upload.failed_count', { count: failedCount }) : t('common.done')}</span>
            </button>
            <div className="sc-upload-tray__actions">
              <IconButton label={t('common.clear_finished_items')} onClick={() => uploads.clearFinished()}><Icon name="check" /></IconButton>
              <IconButton label={open ? t('common.collapse') : t('common.expand')} expanded={open} onClick={() => uploads.setOpen(!open)}><Icon name={open ? 'chevron_right' : 'chevron_left'} /></IconButton>
            </div>
          </header>
          {open ? (
            <div className="sc-upload-tray__scroll">
              <VirtualList
                className="sc-upload-tray__list"
                items={items}
                itemKey={(item) => item.id}
                estimateSize={120}
                itemProps={() => ({ className: 'sc-upload-tray__item' })}
                renderItem={(item) => (
                <>
                  <div className="sc-upload-tray__row">
                    <span className="sc-filename sc-upload-tray__name">{item.name}</span>
                    <span className="sc-upload-tray__meta">
                      {formatBytes(item.sent)} / {formatBytes(item.total)}
                      {item.status === 'uploading' ? ` - ${formatRate(item.rate)} - ${formatEta(item.etaSec)}` : ''}
                      {item.status === 'canceled' ? ` - ${t('upload.canceled')}` : ''}
                      {item.status === 'paused' ? ` - ${t('upload.paused')}` : ''}
                    </span>
                  </div>
                  <mdui-linear-progress value={item.total > 0 ? Math.min(Math.max(item.sent / item.total, 0), 1) : 0} aria-label={item.name}></mdui-linear-progress>
                  {item.message ? <p className="sc-upload-tray__message">{t(item.message, item.messageParams)}</p> : null}
                  <div className="sc-upload-tray__controls">
                    {item.status === 'uploading' ? <IconButton label={t('upload.pause')} onClick={() => pauseUpload(item.id)}><Icon name="pause" /></IconButton> : null}
                    {item.status === 'paused' ? <IconButton label={t('upload.resume')} onClick={() => resumeUpload(item.id)}><Icon name="play_arrow" /></IconButton> : null}
                    {item.status === 'done' || item.status === 'canceled' || item.status === 'error' ? (
                      <IconButton label={t('common.clear')} onClick={() => uploads.dismiss(item.id)}><Icon name="close" /></IconButton>
                    ) : (
                      <IconButton label={t('common.cancel')} onClick={() => cancelUpload(item.id)}><Icon name="close" /></IconButton>
                    )}
                  </div>
                </>
                )}
              />
            </div>
          ) : null}
        </section>
      ) : null}
    </>
  )
}
