import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { overlay } from 'overlay-kit'
import { useI18n } from '../../../hooks/use-i18n'
import { useHostListing } from '../../admin/api'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { Icon } from '../../../ui/Icon'
import { VirtualList } from '../../../ui/VirtualList'
import * as styles from './PathPickerDialog.css'
import * as utilitiesStyles from '../../../ui/utilities.css'
import { cx } from '../../../ui/cx'

export interface PathPickerOptions {
  mode: 'folder' | 'file'
  start?: string
  /** Browses through the setup endpoint, before there is an account to sign in with. */
  token?: string
}

export interface PathPickerDialogProps extends PathPickerOptions {
  open: boolean
  onClose: () => void
  onPick: (path: string) => void
  onClosed?: () => void
}

/** Lets the user browse the server's disk for a folder or a file. Null when cancelled. */
export function pickPath(options: PathPickerOptions): Promise<string | null> {
  return overlay.openAsync<string | null>(({ isOpen, close, unmount }) => (
    <PathPickerDialog {...options} open={isOpen} onClose={() => close(null)} onPick={close} onClosed={unmount} />
  ))
}

function guessStart(start: string | undefined, mode: PathPickerOptions['mode']): string {
  if (!start) return ''
  if (mode === 'folder') return start
  const cut = start.lastIndexOf('/')
  return cut > 0 ? start.slice(0, cut) : ''
}

export function PathPickerDialog({ open, mode, start, token, onClose, onPick, onClosed }: PathPickerDialogProps) {
  const { t } = useI18n()
  const [initialGuess] = useState(() => guessStart(start, mode))
  const [currentPath, setCurrentPath] = useState(initialGuess)
  const [selected, setSelected] = useState<string | null>(null)
  const body = useRef<HTMLDivElement>(null)
  const focusAfterNavigation = useRef(false)
  const listing = useHostListing(token ?? null, currentPath, open)

  useLayoutEffect(() => {
    if (!open || listing.isPlaceholderData || listing.data?.path !== currentPath) return
    if (body.current) body.current.scrollTop = 0
    if (focusAfterNavigation.current) {
      focusAfterNavigation.current = false
      const target = body.current?.querySelector<HTMLElement>('button:not(:disabled)') ?? body.current
      target?.focus({ preventScroll: true })
    }
  }, [open, currentPath, listing.data?.path, listing.isPlaceholderData])

  // A start path that cannot be listed falls back to the roots.
  useEffect(() => {
    if (listing.isError && currentPath === initialGuess && initialGuess !== '') setCurrentPath('')
  }, [listing.isError, currentPath, initialGuess])

  const atRoot = listing.data?.path === ''
  const showError = Boolean(listing.error) && (currentPath !== initialGuess || initialGuess === '')
  const hereText = !listing.data ? '' : atRoot ? t('picker.roots') : `${t('picker.here')}: ${listing.data.path}`
  const title = mode === 'folder' ? t('picker.title_folder') : t('picker.title_file')
  const canConfirm = mode === 'folder' ? Boolean(listing.data && !listing.isError && !atRoot) : selected !== null

  const navigate = (path: string) => {
    focusAfterNavigation.current = body.current?.contains(document.activeElement) ?? false
    setCurrentPath(path)
    setSelected(null)
  }
  const confirm = () => {
    if (mode === 'folder' && listing.data && listing.data.path !== '') onPick(listing.data.path)
    if (mode === 'file' && selected !== null) onPick(selected)
  }

  return (
    <Dialog
      open={open}
      title={title}
      role="dialog"
      dismissible={false}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button disabled={!canConfirm} onClick={confirm}>
            {t('picker.choose')}
          </Button>
        </>
      }
    >
      <div className={styles.root}>
        <div className={styles.nav}>
          <Button
            variant="text"
            disabled={!listing.data || listing.data.parent === ''}
            onClick={() => listing.data && navigate(listing.data.parent)}
          >
            {t('picker.up')}
          </Button>
          <p className={styles.here} aria-live="polite">
            {hereText}
          </p>
        </div>
        <div ref={body} className={styles.body} tabIndex={-1}>
          {listing.isPending ? <p className={styles.status}>{t('common.loading')}</p> : null}
          {showError ? (
            <p className={styles.status} role="alert">
              {t('picker.could_not_list')}
            </p>
          ) : null}
          {!listing.isPending && !showError && listing.data ? (
            listing.data.entries.length === 0 ? (
              <p className={styles.status}>{t('picker.empty')}</p>
            ) : (
              <VirtualList
                key={listing.data.path}
                className={styles.entries}
                aria-label={atRoot ? t('picker.roots') : t('picker.here')}
                items={listing.data.entries}
                itemKey={(entry) => entry.path}
                estimateSize={40}
                renderItem={(entry) =>
                  entry.is_dir ? (
                    <button
                      type="button"
                      className={cx(styles.entry, utilitiesStyles.focusRing)}
                      aria-label={t('picker.open_folder', { name: entry.name })}
                      onClick={() => navigate(entry.path)}
                    >
                      <Icon name="folder" />
                      <span className={styles.entryName}>{entry.name}</span>
                    </button>
                  ) : mode === 'file' ? (
                    <button
                      type="button"
                      className={cx(
                        styles.entry,
                        utilitiesStyles.focusRing,
                        selected === entry.path && styles.entrySelected
                      )}
                      aria-pressed={selected === entry.path}
                      onClick={() => setSelected(entry.path)}
                    >
                      <Icon name="draft" />
                      <span className={styles.entryName}>{entry.name}</span>
                    </button>
                  ) : (
                    <span className={cx(styles.entry, styles.entryDisabled)} aria-disabled="true">
                      <Icon name="draft" />
                      <span className={styles.entryName}>{entry.name}</span>
                    </span>
                  )
                }
              />
            )
          ) : null}
          {listing.data?.truncated ? (
            <p className={styles.status} role="status">
              {t('picker.truncated')}
            </p>
          ) : null}
        </div>
      </div>
    </Dialog>
  )
}
