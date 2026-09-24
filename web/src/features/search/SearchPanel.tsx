import { forwardRef, useImperativeHandle, useRef } from 'react'
import type { FormEvent, KeyboardEvent, ReactNode } from 'react'
import { useI18n } from '../../lib/i18n/use-i18n'
import { Icon } from '../../lib/ui/Icon'
import { useSearchController } from './use-search-controller'
import { useSearchNavigation } from './use-search-navigation'
import { CATEGORIES, SORT_KEYS } from './search-state'
import { SearchResults } from './SearchResults'
import './SearchPanel.css'

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
      <form className="sc-search__query-bar" onSubmit={onSubmit}>
        <span className="sc-search__query-icon" aria-hidden="true"><Icon name="search" size={20} /></span>
        <input
          ref={controller.inputRef}
          className="sc-search__input"
          type="search"
          value={state.query}
          placeholder={t('search.placeholder')}
          autoFocus={autofocus}
          onChange={(event) => controller.set('query', event.target.value)}
          onKeyDown={onQueryKeyDown}
        />
        {state.query.trim() ? <button type="button" className="sc-search__clear-btn sc-icon-button" aria-label={t('search.clear')} onClick={controller.clear}><Icon name="close" size={16} /></button> : null}
        <button type="submit" className="sc-search__submit-btn" aria-label={t('search.run')}>{t('search.run')}</button>
        {trailing}
      </form>

      <div className="sc-search__filter-bar">
        <div className="sc-search__categories" role="tablist" aria-label={t('search.kind_label')} ref={categoriesRef}>
          {CATEGORIES.map((cat) => {
            const isSelected = controller.activeCategory === cat.id
            return <button key={cat.id} type="button" className={`sc-search__category-pill${isSelected ? ' sc-search__category-pill--active' : ''}`} aria-pressed={isSelected} onClick={() => controller.selectCategory(cat.id)}><Icon name={cat.icon} size={15} /><span>{t(cat.labelKey)}</span></button>
          })}
        </div>
        <div className="sc-search__scope-pill" title={scope ? t('search.scope_current_prioritized', { folder: scope }) : t('search.scope_explanation')}><Icon name={scope ? 'folder' : 'search'} size={14} /><span>{scope ? (scope.split('/').filter(Boolean).at(-1) ?? scope) : t('search.scope_all_accessible')}</span></div>
        <div className="sc-search__sort-wrap">
          <button type="button" className="sc-search__sort-btn" aria-expanded={state.sortOpen} aria-label={t('search.sort_by', { key: sortLabel })} onClick={() => controller.set('sortOpen', (open) => !open)}><Icon name="sort" size={15} /><span>{sortLabel}</span></button>
          {state.sortOpen ? <div className="sc-search__menu sc-search__menu--end" role="menu">{SORT_KEYS.map(([key, labelKey]) => <button key={key} type="button" role="menuitemradio" aria-checked={state.sortKey === key} onClick={() => { controller.set('sortKey', key); controller.set('sortOpen', false) }}>{state.sortKey === key ? '✓ ' : ''}{t(labelKey)}</button>)}</div> : null}
        </div>
      </div>

      <div className="sc-search__status-bar">
        <span className="sc-search__status-info" aria-hidden={state.running}>
          {state.running ? <span className="sc-search__progress" role="img" aria-label={t('search.searching_label')}><mdui-circular-progress /></span> : null}
          <span>{controller.status.key ? t(controller.status.key, controller.status.values) : ''}{state.running && state.scanned ? `, ${t('search.scanning', { dirs: String(state.scanned.dirs) })}` : ''}</span>
        </span>
        <span className="sc-search__spoken" role="status" aria-live="polite">{state.running ? '' : controller.status.key ? t(controller.status.key, controller.status.values) : ''}</span>
        <span className="sc-search__status-actions">
          {!state.running && controller.fileCount > 0 && controller.dirCount > 0 ? <span className="sc-search__breakdown">{t('search.summary', { files: String(controller.fileCount), folders: String(controller.dirCount) })}</span> : null}
          {state.running ? <button type="button" className="sc-search__stop-btn" onClick={controller.stop}>{t('search.stop')}</button> : null}
        </span>
      </div>

      <SearchResults ran={state.ran} running={state.running} view={controller.view} rows={controller.rows} windowed={controller.windowed} activeFilters={controller.activeFilters} onOpen={navigation.openResult} onScroll={(scrollTop) => controller.set('scrollTop', scrollTop)} resultsRef={(node) => { resultsContainer.current = node }} t={t} />
    </div>
  )
})
