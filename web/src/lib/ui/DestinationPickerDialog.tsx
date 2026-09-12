import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { destinationProblem } from '../api/path-utils'
import { statQuery } from '../query/files'
import { sessionQuery } from '../query/session'
import { useI18n } from '../i18n/use-i18n'
import { Button } from './Button'
import { BrowseDialog } from './browse-dialog'
import { FileTreeList } from './FileTree'
export function DestinationPickerDialog({
  open,
  sources,
  canCopy,
  canMove,
  onClose,
  onPick
}: {
  open: boolean
  sources: string[]
  canCopy: boolean
  canMove: boolean
  onClose: () => void
  onPick: (dest: string, mode: 'move' | 'copy') => void
}) {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const roots = useMemo(() => (session.data?.roots ?? []).map((root) => ({ path: `/${root.label}`, name: root.label })), [session.data?.roots])
  const [selected, setSelected] = useState<string | null>(null)
  const stat = useQuery({ ...statQuery(selected ?? ''), enabled: open && selected !== null })
  const problem = selected ? destinationProblem(selected, sources) : null
  const writable = stat.data?.perms.create ?? false
  const copy = canCopy && selected !== null && problem !== 'into_itself' && writable
  const move = canMove && selected !== null && problem === null && writable
  const isWarn = problem !== null || (selected !== null && stat.data && !writable)

  useEffect(() => {
    if (!open) {
      setSelected(null)
    }
  }, [open])

  return (
    <BrowseDialog
      open={open}
      title={t('dest.move_or_copy')}
      onClose={onClose}
      actions={
        <>
          <Button variant="text" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          {canCopy ? (
            <Button variant="outlined" disabled={!copy} onClick={() => selected && onPick(selected, 'copy')}>
              {t('common.copy')}
            </Button>
          ) : null}
          {canMove ? (
            <Button disabled={!move} onClick={() => selected && onPick(selected, 'move')}>
              {t('common.move')}
            </Button>
          ) : null}
        </>
      }
    >
      <div className="sc-dest">
        <p className="sc-dest__prompt">{t('dest.choose_destination_folder', { count: sources.length })}</p>
        <div className="sc-dest__tree">
          <FileTreeList
            roots={roots}
            currentPath={selected ?? ''}
            onNavigate={setSelected}
            aria-label={t('dest.destination_folder')}
          />
        </div>
        <p className={`sc-dest__status${isWarn ? ' sc-dest__status--warn' : ''}`} aria-live="polite">
          {problem === 'into_itself'
            ? t('dest.cannot_move_folder_into_itself')
            : problem === 'same_folder'
              ? t('dest.already_in_this_folder')
              : selected !== null && stat.data && !writable
                ? t('dest.cannot_write_into_folder')
                : selected ?? t('dest.no_folder_chosen')}
        </p>
      </div>
    </BrowseDialog>
  )
}
