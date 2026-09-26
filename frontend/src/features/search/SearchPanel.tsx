import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import type { FormEvent, KeyboardEvent, ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { Icon } from '../../lib/ui/Icon'
import { useSearchController } from './hooks/use-search-controller'
import { useSearchNavigation } from './hooks/use-search-navigation'
import { CATEGORIES, SORT_KEYS } from './logic/search-state'
import { SearchResults } from './SearchResults'
import '../../styles/features/search/search-panel.css.ts'

export interface SearchPanelHandle { focus: () => void }

export interface SearchPanelProps {
  readonly scope?: string
  readonly autofocus?: boolean
  readonly onnavigated?: () => void
  readonly trailing?: ReactNode
}

export const SearchPanel = forwardRef<SearchPanelHandle, SearchPanelProps>(function SearchPanel({ scope = '', autofocus = false, onnavigated, trailing }, ref) {
  const { t } = useI18n()
  const resultsContainer = useRef<HTMLDivElement | null>(null)
  const categoriesRef = useRef<HTMLDivElement | null>(null)
  const controller = useSearchController({ scope, resultsContainer, categoriesRef })
  const navigation = useSearchNavigation({ scope, state: controller.state, onNavigated: onnavigated })
  const { state } = controller
  useImperativeHandle(ref, () => ({ focus: () => controller.inputRef.current?.focus() }), [controller.inputRef])
  const [spokenStatus, setSpokenStatus] = useState('')
  const lastSpokenAt = useRef(0)
  useEffect(() => {
    const text = state.running
      ? `${t('search.searching_label')}${state.scanned ? `, ${t('search.scanning', { dirs: String(state.scanned.dirs) })}` : ''}`
      : controller.status.key ? t(controller.status.key, controller.status.values) : ''
    const now = Date.now()
    if (state.running && now - lastSpokenAt.current < 1000) return
    lastSpokenAt.current = now
    setSpokenStatus(text)
  }, [controller.status.key, controller.status.values, state.running, state.scanned, t])

  const onSubmit = (event: FormEvent): void => {
    event.preventDefault()
    controller.start()
  }
  const onQueryKeyDown = (event: KeyboardEvent<HTMLInputElement>): void => {
    if (event.key !== 'Enter') return
    event.preventDefault()
    controller.start()
  }
  const sortLabel = t(controller.sortLabelKey)

  return (
    <div className="sc-search">
      <form className="sc-search-query-bar" onSubmit={onSubmit}>
        <span className="sc-search-query-icon" aria-hidden="true"><Icon name="search" size={20} /></span>
        <input
          ref={controller.inputRef}
          className="sc-search-input"
          type="search"
          value={state.query}
          placeholder={t('search.placeholder')}
          autoFocus={autofocus}
          onChange={(event) => controller.set('query', event.target.value)}
          onKeyDown={onQueryKeyDown}
        />
        {state.query.trim() ? <button type="button" className="sc-search-clear-btn sc-icon-button" aria-label={t('search.clear')} onClick={controller.clear}><Icon name="close" size={16} /></button> : null}
        <button type="submit" className="sc-search-submit-btn" aria-label={t('search.run')}>{t('search.run')}</button>
        {trailing}
      </form>

      <div className="sc-search-filter-bar">
        <div className="sc-search-categories" role="tablist" aria-label={t('search.kind_label')} ref={categoriesRef}>
          {CATEGORIES.map((cat) => {
            const isSelected = controller.activeCategory === cat.id
            return <button key={cat.id} type="button" className={`sc-search-category-pill${isSelected ? ' sc-search-category-pill-active' : ''}`} aria-pressed={isSelected} onClick={() => controller.selectCategory(cat.id)}><Icon name={cat.icon} size={15} /><span>{t(cat.labelKey)}</span></button>
          })}
        </div>
        <div className="sc-search-scope-pill" title={scope ? t('search.scope_current_prioritized', { folder: scope }) : t('search.scope_explanation')}><Icon name={scope ? 'folder' : 'search'} size={14} /><span>{scope ? (scope.split('/').filter(Boolean).at(-1) ?? scope) : t('search.scope_all_accessible')}</span></div>
        <div className="sc-search-sort-wrap">
          <button type="button" className="sc-search-sort-btn" aria-expanded={state.sortOpen} aria-label={t('search.sort_by', { key: sortLabel })} onClick={() => controller.set('sortOpen', (open) => !open)}><Icon name="sort" size={15} /><span>{sortLabel}</span></button>
          {state.sortOpen ? <div className="sc-search-menu sc-search-menu-end" role="menu">{SORT_KEYS.map(([key, labelKey]) => <button key={key} type="button" role="menuitemradio" aria-checked={state.sortKey === key} onClick={() => { controller.set('sortKey', key); controller.set('sortOpen', false) }}>{state.sortKey === key ? '✓ ' : ''}{t(labelKey)}</button>)}</div> : null}
        </div>
      </div>

      <div className="sc-search-status-bar">
        <span className="sc-search-status-info" aria-hidden={state.running}>
          {state.running ? <span className="sc-search-progress" role="img" aria-label={t('search.searching_label')}><mdui-circular-progress /></span> : null}
          <span>{controller.status.key ? t(controller.status.key, controller.status.values) : ''}{state.running && state.scanned ? `, ${t('search.scanning', { dirs: String(state.scanned.dirs) })}` : ''}</span>
        </span>
        <span className="sc-search-spoken" role="status" aria-live="polite">{spokenStatus}</span>
        <span className="sc-search-status-actions">
          {!state.running && controller.fileCount > 0 && controller.dirCount > 0 ? <span className="sc-search-breakdown">{t('search.summary', { files: String(controller.fileCount), folders: String(controller.dirCount) })}</span> : null}
          {state.running ? <button type="button" className="sc-search-stop-btn" onClick={controller.stop}>{t('search.stop')}</button> : null}
        </span>
      </div>

      <SearchResults ran={state.ran} running={state.running} view={controller.view} rows={controller.rows} windowed={controller.windowed} activeFilters={controller.activeFilters} onOpen={navigation.openResult} onScroll={(scrollTop) => controller.set('scrollTop', scrollTop)} resultsRef={(node) => { resultsContainer.current = node }} t={t} />
    </div>
  )
})
