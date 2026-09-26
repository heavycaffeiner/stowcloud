import { useEffect, useLayoutEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useComponentState } from '../../lib/store/use-component-state'
import { useI18n } from '../../lib/i18n/use-i18n'
import { api } from '../../lib/api/client'
import { Button } from '../../lib/ui/Button'
import '../../styles/features/files/path-picker.css.ts'
import { Icon } from '../../lib/ui/Icon'
import { VirtualList } from '../../lib/ui/VirtualList'

export interface PathPickerDialogProps {
  open: boolean
  mode: 'folder' | 'file'
  start?: string
  token?: string
  onclose: () => void
  onpick: (path: string) => void
}

function guessStart(start: string | undefined, mode: PathPickerDialogProps['mode']): string {
  if (!start) return ''
  if (mode === 'folder') return start
  const cut = start.lastIndexOf('/')
  return cut > 0 ? start.slice(0, cut) : ''
}
export function PathPickerDialog({ open, mode, start, token, onclose, onpick }: PathPickerDialogProps) {
  const { t } = useI18n()
  const [state, setState] = useComponentState({ currentPath: '', initialGuess: '', selected: null as string | null, wasOpen: false })
  const { currentPath, initialGuess, selected, wasOpen } = state
  const body = useRef<HTMLDivElement>(null)
  const focusAfterNavigation = useRef(false)

  useEffect(() => {
    if (open && !wasOpen) {
      const guess = guessStart(start, mode)
      setState({ currentPath: guess, initialGuess: guess, selected: null, wasOpen: true })
    } else {
      setState((value) => value.wasOpen === open ? value : { ...value, wasOpen: open })
    }
  }, [open, wasOpen, start, mode, setState])

  const listing = useQuery({
    queryKey: ['host-fs', token ?? null, currentPath],
    queryFn: () => token ? api.browseSetupPath(token, currentPath) : api.browseHostPath(currentPath),
    enabled: open,
    retry: false,
    placeholderData: (previous) => previous
  })

  useLayoutEffect(() => {
    if (!open || listing.isPlaceholderData || listing.data?.path !== currentPath) return
    if (body.current) body.current.scrollTop = 0
    if (focusAfterNavigation.current) {
      focusAfterNavigation.current = false
      const target = body.current?.querySelector<HTMLElement>('button:not(:disabled)') ?? body.current
      target?.focus({ preventScroll: true })
    }
  }, [open, currentPath, listing.data?.path, listing.isPlaceholderData])

  useEffect(() => {
    if (open && listing.isError && currentPath === initialGuess && initialGuess !== '') {
      setState((value) => ({ ...value, currentPath: '' }))
    }
  }, [open, listing.isError, currentPath, initialGuess, setState])

  const atRoot = listing.data?.path === ''
  const showError = Boolean(listing.error) && (currentPath !== initialGuess || initialGuess === '')
  const hereText = !listing.data ? '' : atRoot ? t('picker.roots') : `${t('picker.here')}: ${listing.data.path}`
  const title = mode === 'folder' ? t('picker.title_folder') : t('picker.title_file')
  const canConfirm = mode === 'folder' ? Boolean(listing.data && !listing.isError && !atRoot) : selected !== null

  const navigate = (path: string) => {
    focusAfterNavigation.current = body.current?.contains(document.activeElement) ?? false
    setState((value) => ({ ...value, currentPath: path, selected: null }))
  }
  const confirm = () => {
    if (mode === 'folder' && listing.data && listing.data.path !== '') onpick(listing.data.path)
    if (mode === 'file' && selected !== null) onpick(selected)
  }

  return (
    <mdui-dialog open={open} headline={title} close-on-esc={false} close-on-overlay-click={false}>
      <div className="sc-picker">
        <div className="sc-picker-nav">
          <Button
            variant="text"
            disabled={!listing.data || listing.data.parent === ''}
            onClick={() => listing.data && navigate(listing.data.parent)}
          >
            {t('picker.up')}
          </Button>
          <p className="sc-picker-here" aria-live="polite">{hereText}</p>
        </div>
        <div ref={body} className="sc-picker-body" tabIndex={-1}>
          {listing.isPending ? <p className="sc-picker-status">{t('common.loading')}</p> : null}
          {showError ? <p className="sc-picker-status" role="alert">{t('picker.could_not_list')}</p> : null}
          {!listing.isPending && !showError && listing.data ? (
            listing.data.entries.length === 0 ? <p className="sc-picker-status">{t('picker.empty')}</p> : (
              <VirtualList
                key={listing.data.path}
                className="sc-picker-entries"
                aria-label={atRoot ? t('picker.roots') : t('picker.here')}
                items={listing.data.entries}
                itemKey={(entry) => entry.path}
                estimateSize={40}
                renderItem={(entry) => entry.is_dir ? (
                      <button
                        type="button"
                        className="sc-picker-entry sc-focus-ring"
                        aria-label={t('picker.open_folder', { name: entry.name })}
                        onClick={() => navigate(entry.path)}
                      >
                        <Icon name="folder" />
                        <span>{entry.name}</span>
                      </button>
                    ) : mode === 'file' ? (
                      <button
                        type="button"
                        className={`sc-picker-entry sc-focus-ring${selected === entry.path ? ' sc-picker-entry-selected' : ''}`}
                        aria-pressed={selected === entry.path}
                        onClick={() => setState((value) => ({ ...value, selected: entry.path }))}
                      >
                        <Icon name="draft" />
                        <span>{entry.name}</span>
                      </button>
                    ) : (
                      <span className="sc-picker-entry sc-picker-entry-disabled" aria-disabled="true">
                        <Icon name="draft" />
                        <span>{entry.name}</span>
                      </span>
                    )}
              />
            )
          ) : null}
          {listing.data?.truncated ? <p className="sc-picker-status" role="status">{t('picker.truncated')}</p> : null}
        </div>
      </div>
      <mdui-button slot="action" variant="text" onClick={onclose}>{t('common.cancel')}</mdui-button>
      <mdui-button slot="action" variant="filled" disabled={!canConfirm} onClick={confirm}>{t('picker.choose')}</mdui-button>
    </mdui-dialog>
  )
}
