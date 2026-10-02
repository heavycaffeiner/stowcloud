import { useRef } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { cx, Icon } from '@/shared/ui'
import * as styles from './FileTreeItem.css'

export interface FileTreeItemProps {
  path: string
  name: string
  depth: number
  active: boolean
  ancestor: boolean
  expanded: boolean
  tabIndex: number
  /** Makes this row the one a surrounding dialog focuses first. */
  autoFocus?: boolean
  onNavigate: (path: string) => void
  onToggle: (path: string) => void
}

export function FileTreeItem({
  path,
  name,
  depth,
  active,
  ancestor,
  expanded,
  tabIndex,
  autoFocus = false,
  onNavigate,
  onToggle
}: FileTreeItemProps) {
  const { t } = useI18n()
  const label = useRef<HTMLButtonElement>(null)

  return (
    <div
      className={cx(styles.root, active && styles.active, ancestor && styles.ancestor)}
      style={{ paddingInlineStart: depth * 16 + 8 }}
    >
      <button
        type="button"
        className={styles.twisty}
        data-tree-toggle
        tabIndex={-1}
        aria-expanded={expanded}
        aria-label={expanded ? t('tree.collapse', { name }) : t('tree.expand', { name })}
        onClick={() => {
          label.current?.focus()
          onToggle(path)
        }}
      >
        <span className={cx(styles.twistyIcon, expanded && styles.twistyIconExpanded)} aria-hidden="true">
          <Icon name="chevron_right" size={16} />
        </span>
      </button>
      <button
        ref={label}
        type="button"
        className={styles.label}
        data-tree-label
        data-autofocus={autoFocus || undefined}
        tabIndex={tabIndex}
        onClick={() => onNavigate(path)}
      >
        <Icon name="folder" className={styles.icon} />
        <span className={styles.name}>{name}</span>
      </button>
    </div>
  )
}
