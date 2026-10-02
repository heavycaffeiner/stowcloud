import { useEffect, useRef, useState } from 'react'
import type { FormEvent, KeyboardEvent, ReactNode } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { useOpenedSearch, useSubmittedQuery } from '../state'
import {
  Icon,
  StowBadge,
  StowButton,
  StowIconButton,
  StowMenuButton,
  StowMenuItem,
  StowProgressCircular,
  StowTextField
} from '@/shared/ui'
import { useSearchController } from '../hooks/use-search-controller'
import { useSearchNavigation } from '../hooks/use-search-navigation'
import { CATEGORIES, SORT_KEYS } from '../logic/search-state'
import { SearchResults } from './SearchResults'
import * as styles from './SearchPanel.css'
import { srOnly } from '@/shared/theme'

export interface SearchPanelProps {
  readonly scope?: string
  readonly autoFocus?: boolean
  readonly trailing?: ReactNode
}

export function SearchPanel({ scope = '', autoFocus = false, trailing }: SearchPanelProps) {
  const { t } = useI18n()
  const resultsContainer = useRef<HTMLDivElement | null>(null)
  const categoriesRef = useRef<HTMLDivElement | null>(null)
  const { query: submitted } = useOpenedSearch()
  const recordQuery = useSubmittedQuery()
  const controller = useSearchController({ scope, submitted, resultsContainer, categoriesRef })
  const navigation = useSearchNavigation({ scope, state: controller.state })
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

  const run = (): void => {
    controller.start()
    recordQuery(state.query.trim())
  }
  const clear = (): void => {
    controller.clear()
    recordQuery('')
  }
  const onSubmit = (event: FormEvent): void => {
    event.preventDefault()
    run()
  }
  const onQueryKeyDown = (event: KeyboardEvent<HTMLInputElement>): void => {
    if (event.key !== 'Enter') return
    event.preventDefault()
    run()
  }
  const sortLabel = t(controller.sortLabelKey)

  return (
    <div className={styles.root}>
      <form className={styles.queryBar} onSubmit={onSubmit}>
        <StowTextField
          ref={controller.inputRef}
          className={styles.queryField}
          leftSection={<Icon name="search" size={20} />}
          role="searchbox"
          inputMode="search"
          enterKeyHint="search"
          aria-label={t('search.placeholder')}
          value={state.query}
          placeholder={t('search.placeholder')}
          autoFocus={autoFocus}
          onValueChange={(value) => controller.set('query', value)}
          onKeyDown={onQueryKeyDown}
        />
        {state.query.trim() ? <StowIconButton label={t('search.clear')} icon="close" onClick={clear} /> : null}
        <StowButton type="submit" variant="text">
          {t('search.run')}
        </StowButton>
        {trailing}
      </form>

      <div className={styles.filterBar}>
        <div className={styles.categories} role="group" aria-label={t('search.kind_label')} ref={categoriesRef}>
          {CATEGORIES.map((cat) => {
            const isSelected = controller.activeCategory === cat.id
            return (
              <StowButton
                key={cat.id}
                variant={isSelected ? 'tonal' : 'outlined'}
                pressed={isSelected}
                icon={<Icon name={cat.icon} size={18} />}
                className={styles.categoryPill}
                onClick={() => controller.selectCategory(cat.id)}
              >
                {t(cat.labelKey)}
              </StowButton>
            )
          })}
        </div>
        <StowBadge
          tone="accent"
          icon={scope ? 'folder' : 'search'}
          className={styles.scope}
          title={scope ? t('search.scope_current_prioritized', { folder: scope }) : t('search.scope_explanation')}
        >
          {scope ? (scope.split('/').filter(Boolean).at(-1) ?? scope) : t('search.scope_all_accessible')}
        </StowBadge>
        <StowMenuButton
          label={t('search.sort_by', { key: sortLabel })}
          align="end"
          menu={(close) =>
            SORT_KEYS.map(([key, labelKey]) => (
              <StowMenuItem
                key={key}
                checked={state.sortKey === key}
                onClick={() => {
                  controller.set('sortKey', key)
                  close()
                }}
              >
                {t(labelKey)}
              </StowMenuItem>
            ))
          }
        >
          <StowButton
            variant="text"
            className={styles.sort}
            icon={<Icon name="sort" size={18} />}
            aria-label={t('search.sort_by', { key: sortLabel })}
          >
            {sortLabel}
          </StowButton>
        </StowMenuButton>
      </div>

      <div className={styles.statusBar}>
        <span className={styles.statusInfo} aria-hidden={state.running}>
          {state.running ? (
            <span className={styles.progress} role="img" aria-label={t('search.searching_label')}>
              <StowProgressCircular size={14} />
            </span>
          ) : null}
          <span>
            {controller.status.key ? t(controller.status.key, controller.status.values) : ''}
            {state.running && state.scanned ? `, ${t('search.scanning', { dirs: String(state.scanned.dirs) })}` : ''}
          </span>
        </span>
        <span className={srOnly} role="status" aria-live="polite">
          {spokenStatus}
        </span>
        <span className={styles.statusActions}>
          {!state.running && controller.fileCount > 0 && controller.dirCount > 0 ? (
            <span>
              {t('search.summary', { files: String(controller.fileCount), folders: String(controller.dirCount) })}
            </span>
          ) : null}
          {state.running ? (
            <StowButton variant="text" danger onClick={controller.stop}>
              {t('search.stop')}
            </StowButton>
          ) : null}
        </span>
      </div>

      <SearchResults
        ran={state.ran}
        running={state.running}
        view={controller.view}
        activeFilters={controller.activeFilters}
        onOpen={navigation.openResult}
        onScroll={(scrollTop) => controller.set('scrollTop', scrollTop)}
        resultsRef={resultsContainer}
      />
    </div>
  )
}
