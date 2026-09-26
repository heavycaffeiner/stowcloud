import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { destinationProblem } from '../../lib/api/path-utils'
import { statQuery } from '../../lib/query/files'
import { sessionQuery } from '../../lib/query/session'
import { useComponentState } from '../../lib/store/use-component-state'
import { useI18n } from '../../lib/i18n/use-i18n'
import { Button } from '../../lib/ui/Button'
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
  const [state, setState] = useComponentState<{ selected: string | null }>({ selected: null })
  const selected = state.selected
  const stat = useQuery({ ...statQuery(selected ?? ''), enabled: open && selected !== null })
  const problem = selected ? destinationProblem(selected, sources) : null
  const writable = stat.data?.perms.create ?? false
  const copy = canCopy && selected !== null && problem !== 'into_itself' && writable
  const move = canMove && selected !== null && problem === null && writable
  const isWarn = problem !== null || (selected !== null && stat.data && !writable)

  useEffect(() => {
    if (!open) {
      setState({ selected: null })
    }
  }, [open, setState])

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
        <p className="sc-dest-prompt">{t('dest.choose_destination_folder', { count: sources.length })}</p>
        <div className="sc-dest-tree">
          <FileTreeList
            roots={roots}
            currentPath={selected ?? ''}
            onNavigate={(selected) => setState({ selected })}
            aria-label={t('dest.destination_folder')}
          />
        </div>
        <p className={`sc-dest-status${isWarn ? ' sc-dest-status--warn' : ''}`} aria-live="polite">
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
