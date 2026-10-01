import { useEffect, useRef, useState } from 'react'
import type { FormEvent, KeyboardEvent, ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { Icon } from '../../ui/Icon'
import { useSearchController } from './hooks/use-search-controller'
import { useSearchNavigation } from './hooks/use-search-navigation'
import { CATEGORIES, SORT_KEYS } from './logic/search-state'
import { SearchResults } from './SearchResults'
import { ProgressCircular } from '../../ui/ProgressCircular'
import * as styles from './SearchPanel.css'
import * as utilitiesStyles from '../../ui/utilities.css'
import * as iconButtonStyles from '../../ui/IconButton.css'
import { cx } from '../../ui/cx'

export interface SearchPanelProps {
  readonly scope?: string
  readonly autoFocus?: boolean
  readonly onNavigated?: () => void
  readonly trailing?: ReactNode
}

export function SearchPanel({ scope = '', autoFocus = false, onNavigated, trailing }: SearchPanelProps) {
  const { t } = useI18n()
  const resultsContainer = useRef<HTMLDivElement | null>(null)
  const categoriesRef = useRef<HTMLDivElement | null>(null)
  const controller = useSearchController({ scope, resultsContainer, categoriesRef })
  const navigation = useSearchNavigation({ scope, state: controller.state, onNavigated })
  const { state } = controller
  const [spokenStatus, setSpokenStatus] = useState('')
  const lastSpokenAt = useRef(0)
  useEffect(() => {
    const text = state.running
      ? `${t('search.searching_label')}${state.scanned ? `, ${t('search.scanning', { dirs: String(state.scanned.dirs) })}` : ''}`
      : controller.status.key
        ? t(controller.status.key, controller.status.values)
        : ''
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
    <div className={styles.root}>
      <form className={styles.queryBar} onSubmit={onSubmit}>
        <span className={styles.queryIcon} aria-hidden="true">
          <Icon name="search" size={20} />
        </span>
        <input
          ref={controller.inputRef}
          className={styles.input}
          type="text"
          role="searchbox"
          inputMode="search"
          enterKeyHint="search"
          aria-label={t('search.placeholder')}
          value={state.query}
          placeholder={t('search.placeholder')}
          autoFocus={autoFocus}
          onChange={(event) => controller.set('query', event.target.value)}
          onKeyDown={onQueryKeyDown}
        />
        {state.query.trim() ? (
          <button
            type="button"
            className={cx(styles.clearBtn, iconButtonStyles.root)}
            aria-label={t('search.clear')}
            onClick={controller.clear}
          >
            <Icon name="close" size={16} />
          </button>
        ) : null}
        <button type="submit" className={styles.submitBtn} aria-label={t('search.run')}>
          {t('search.run')}
        </button>
        {trailing}
      </form>

      <div className={styles.filterBar}>
        <div className={styles.categories} role="group" aria-label={t('search.kind_label')} ref={categoriesRef}>
          {CATEGORIES.map((cat) => {
            const isSelected = controller.activeCategory === cat.id
            return (
              <button
                key={cat.id}
                type="button"
                className={cx(styles.categoryPill, isSelected && styles.categoryPillActive)}
                aria-pressed={isSelected}
                onClick={() => controller.selectCategory(cat.id)}
              >
                <Icon name={cat.icon} size={15} />
                <span>{t(cat.labelKey)}</span>
              </button>
            )
          })}
        </div>
        <div
          className={styles.scopePill}
          title={scope ? t('search.scope_current_prioritized', { folder: scope }) : t('search.scope_explanation')}
        >
          <Icon name={scope ? 'folder' : 'search'} size={14} />
          <span>{scope ? (scope.split('/').filter(Boolean).at(-1) ?? scope) : t('search.scope_all_accessible')}</span>
        </div>
        <div className={styles.sortWrap}>
          <button
            type="button"
            className={styles.sortBtn}
            aria-expanded={state.sortOpen}
            aria-label={t('search.sort_by', { key: sortLabel })}
            onClick={() => controller.set('sortOpen', (open) => !open)}
          >
            <Icon name="sort" size={15} />
            <span>{sortLabel}</span>
          </button>
          {state.sortOpen ? (
            <div className={styles.menu} role="menu">
              {SORT_KEYS.map(([key, labelKey]) => (
                <button
                  key={key}
                  type="button"
                  role="menuitemradio"
                  className={styles.menuItem}
                  aria-checked={state.sortKey === key}
                  onClick={() => {
                    controller.set('sortKey', key)
                    controller.set('sortOpen', false)
                  }}
                >
                  {state.sortKey === key ? '✓ ' : ''}
                  {t(labelKey)}
                </button>
              ))}
            </div>
          ) : null}
        </div>
      </div>

      <div className={styles.statusBar}>
        <span className={styles.statusInfo} aria-hidden={state.running}>
          {state.running ? (
            <span className={styles.progress} role="img" aria-label={t('search.searching_label')}>
              <ProgressCircular size={14} />
            </span>
          ) : null}
          <span>
            {controller.status.key ? t(controller.status.key, controller.status.values) : ''}
            {state.running && state.scanned ? `, ${t('search.scanning', { dirs: String(state.scanned.dirs) })}` : ''}
          </span>
        </span>
        <span className={utilitiesStyles.srOnly} role="status" aria-live="polite">
          {spokenStatus}
        </span>
        <span className={styles.statusActions}>
          {!state.running && controller.fileCount > 0 && controller.dirCount > 0 ? (
            <span>
              {t('search.summary', { files: String(controller.fileCount), folders: String(controller.dirCount) })}
            </span>
          ) : null}
          {state.running ? (
            <button type="button" className={styles.stopBtn} onClick={controller.stop}>
              {t('search.stop')}
            </button>
          ) : null}
        </span>
      </div>

      <SearchResults
        ran={state.ran}
        running={state.running}
        view={controller.view}
        rows={controller.rows}
        windowed={controller.windowed}
        activeFilters={controller.activeFilters}
        onOpen={navigation.openResult}
        onScroll={(scrollTop) => controller.set('scrollTop', scrollTop)}
        resultsRef={(node) => {
          resultsContainer.current = node
        }}
      />
    </div>
  )
}
