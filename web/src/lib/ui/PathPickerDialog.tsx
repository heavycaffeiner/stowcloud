import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { HostListing } from '../api/types'
import { t } from '../i18n'
import { api } from '../api/client'
import { Button } from './Button'
import './path-picker.css'
import { Icon } from './Icon'

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
  const [currentPath, setCurrentPath] = useState('')
  const [initialGuess, setInitialGuess] = useState('')
  const [selected, setSelected] = useState<string | null>(null)
  const [wasOpen, setWasOpen] = useState(false)

  useEffect(() => {
    if (open && !wasOpen) {
      const guess = guessStart(start, mode)
      setCurrentPath(guess)
      setInitialGuess(guess)
      setSelected(null)
    }
    setWasOpen(open)
  }, [open, wasOpen, start, mode])

  const listing = useQuery({
    queryKey: ['host-fs', token ?? null, currentPath],
    queryFn: () => token ? api.browseSetupPath(token, currentPath) : api.browseHostPath(currentPath),
    enabled: open,
    retry: false,
    placeholderData: (previous) => previous
  })

  useEffect(() => {
    if (open && listing.isError && currentPath === initialGuess && initialGuess !== '') {
      setCurrentPath('')
    }
  }, [open, listing.isError, currentPath, initialGuess])

  const atRoot = listing.data?.path === ''
  const showError = Boolean(listing.error) && (currentPath !== initialGuess || initialGuess === '')
  const hereText = useMemo(() => {
    if (!listing.data) return ''
    return atRoot ? t('picker.roots') : `${t('picker.here')}: ${listing.data.path}`
  }, [listing.data, atRoot])
  const title = mode === 'folder' ? t('picker.title_folder') : t('picker.title_file')
  const canConfirm = mode === 'folder' ? Boolean(listing.data && !listing.isError && !atRoot) : selected !== null

  const navigate = (path: string) => {
    setCurrentPath(path)
    setSelected(null)
  }
  const confirm = () => {
    if (mode === 'folder' && listing.data && listing.data.path !== '') onpick(listing.data.path)
    if (mode === 'file' && selected !== null) onpick(selected)
  }

  return (
    <mdui-dialog open={open} headline={title} close-on-esc={false} close-on-overlay-click={false}>
      <div className="sc-picker">
        <div className="sc-picker__nav">
          <Button
            variant="text"
            disabled={!listing.data || listing.data.parent === ''}
            onClick={() => listing.data && navigate(listing.data.parent)}
          >
            {t('picker.up')}
          </Button>
          <p className="sc-picker__here" aria-live="polite">{hereText}</p>
        </div>
        <div className="sc-picker__body">
          {listing.isPending ? <p className="sc-picker__status">{t('common.loading')}</p> : null}
          {showError ? <p className="sc-picker__status" role="alert">{t('picker.could_not_list')}</p> : null}
          {!listing.isPending && !showError && listing.data ? (
            listing.data.entries.length === 0 ? <p className="sc-picker__status">{t('picker.empty')}</p> : (
              <ul className="sc-picker__entries" aria-label={atRoot ? t('picker.roots') : t('picker.here')}>
                {listing.data.entries.map((entry) => (
                  <li key={entry.path}>
                    {entry.is_dir ? (
                      <button
                        type="button"
                        className="sc-picker__entry sc-focus-ring"
                        aria-label={t('picker.open_folder', { name: entry.name })}
                        onClick={() => navigate(entry.path)}
                      >
                        <Icon name="folder" />
                        <span>{entry.name}</span>
                      </button>
                    ) : mode === 'file' ? (
                      <button
                        type="button"
                        className={`sc-picker__entry sc-focus-ring${selected === entry.path ? ' sc-picker__entry--selected' : ''}`}
                        aria-pressed={selected === entry.path}
                        onClick={() => setSelected(entry.path)}
                      >
                        <Icon name="draft" />
                        <span>{entry.name}</span>
                      </button>
                    ) : (
                      <span className="sc-picker__entry sc-picker__entry--disabled" aria-disabled="true">
                        <Icon name="draft" />
                        <span>{entry.name}</span>
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )
          ) : null}
          {listing.data?.truncated ? <p className="sc-picker__status" role="status">{t('picker.truncated')}</p> : null}
        </div>
      </div>
      <mdui-button slot="action" variant="text" onClick={onclose}>{t('common.cancel')}</mdui-button>
      <mdui-button slot="action" variant="filled" disabled={!canConfirm} onClick={confirm}>{t('picker.choose')}</mdui-button>
    </mdui-dialog>
  )
}
