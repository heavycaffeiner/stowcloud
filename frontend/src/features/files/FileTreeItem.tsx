import { useRef } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { Icon } from '../../lib/ui/Icon'

export interface FileTreeItemProps {
  path: string
  name: string
  depth: number
  active: boolean
  ancestor: boolean
  expanded: boolean
  tabIndex: number
  onNavigate: (path: string) => void
  onToggle: (path: string) => void
}

export function FileTreeItem({ path, name, depth, active, ancestor, expanded, tabIndex, onNavigate, onToggle }: FileTreeItemProps) {
  const { t } = useI18n()
  const label = useRef<HTMLButtonElement>(null)

  return (
    <div
      className={`sc-tree-row${active ? ' sc-tree-row-active' : ''}${ancestor ? ' sc-tree-row-ancestor' : ''}`}
      style={{ paddingInlineStart: depth * 16 + 8 }}
    >
      <button
        type="button"
        className="sc-tree-row-twisty"
        data-tree-toggle
        tabIndex={-1}
        aria-expanded={expanded}
        aria-label={expanded ? t('tree.collapse', { name }) : t('tree.expand', { name })}
        onClick={() => {
          label.current?.focus()
          onToggle(path)
        }}
      >
        <span className={`sc-tree-row-twisty-icon${expanded ? ' sc-tree-row-twisty-icon-expanded' : ''}`} aria-hidden="true">
          <Icon name="chevron_right" size={16} />
        </span>
      </button>
      <button
        ref={label}
        type="button"
        className="sc-tree-row-label"
        data-tree-label
        tabIndex={tabIndex}
        onClick={() => onNavigate(path)}
      >
        <Icon name="folder" />
        <span className="sc-tree-row-name">{name}</span>
      </button>
    </div>
  )
}
