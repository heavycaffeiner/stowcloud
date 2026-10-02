import { useNavigate } from '@tanstack/react-router'
import { useHotkeys } from 'react-hotkeys-hook'
import { useI18n } from '../../hooks/use-i18n'
import { useSession } from '../../features/auth/api'
import { useOpenSearch } from '../../features/search/state'
import { useSignOut } from '../../features/settings/hooks/use-sign-out'
import { cx, Icon, StowIconButton, StowMenuButton, StowMenuItem, StowMenuLabel } from '@/shared/ui'
import { useCompact } from '@/hooks/use-compact'
import { useCurrentFolder, useNavigation } from './navigation'
import { sidebar, toggleSidebar } from './sidebar'
import * as styles from './AppShell.css'
import { focusRing } from '@/shared/theme'

const REPOSITORY = 'https://github.com/heavycaffeiner/Stowcloud'

export function ShellHeader() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const compact = useCompact()
  const files = useNavigation().items[0]
  return (
    <header className={styles.header}>
      <div className={styles.headerLeft}>
        {!compact ? (
          <StowIconButton
            label={t('nav.toggle_sidebar')}
            icon="menu"
            expanded={sidebar.value === 'expanded'}
            onClick={toggleSidebar}
          />
        ) : null}
        <button type="button" className={styles.headerBrandBtn} onClick={() => void navigate({ href: files.href })}>
          <span className={styles.headerBrand}>Stowcloud</span>
        </button>
      </div>

      <div className={styles.headerCenter}>
        <SearchLauncher />
      </div>

      <div className={styles.headerRight}>
        {!compact ? (
          <>
            <StowIconButton
              label={t('nav.help')}
              icon="help"
              onClick={() => window.open(REPOSITORY, '_blank', 'noopener,noreferrer')}
            />
            <StowIconButton
              label={t('common.settings')}
              icon="settings"
              onClick={() => void navigate({ to: '/settings/{-$tab}', params: {} })}
            />
          </>
        ) : null}
        <AccountMenu />
      </div>
    </header>
  )
}

/** The search field stand-in. Ctrl+K opens search from anywhere, `/` from anywhere but a text field or editor. */
function SearchLauncher() {
  const { t } = useI18n()
  const folder = useCurrentFolder()
  const openSearch = useOpenSearch()
  const open = (): void => openSearch(folder === '/' ? '' : folder)
  useHotkeys('ctrl+k, meta+k', open, { preventDefault: true, enableOnFormTags: true, enableOnContentEditable: true })
  // Matched on the character, not the key position: `/` is Shift+7 on some layouts.
  useHotkeys('/', open, {
    useKey: true,
    ignoreModifiers: true,
    ignoreEventWhen: (event) => event.ctrlKey || event.metaKey || event.altKey,
    preventDefault: true
  })
  return (
    <button className={cx(styles.headerSearch, focusRing)} type="button" onClick={open} aria-label={t('common.search')}>
      <span className={styles.headerSearchIcon}>
        <Icon name="search" size={18} />
      </span>
      <span className={styles.headerSearchPlaceholder}>{t('common.search')}</span>
      <span className={styles.headerSearchHints}>
        <kbd className={styles.headerShortcut}>/</kbd>
        <span className={styles.headerFilterIcon} aria-hidden="true">
          <Icon name="tune" size={16} />
        </span>
      </span>
    </button>
  )
}

function AccountMenu() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const user = useSession().data?.user
  const { signOut } = useSignOut()
  const name = user?.display_name || user?.name || t('settings.account')
  return (
    <StowMenuButton
      label={t('settings.account')}
      align="end"
      menu={(close) => (
        <>
          <StowMenuLabel>{name}</StowMenuLabel>
          <StowMenuItem
            icon="settings"
            onClick={() => {
              close()
              void navigate({ to: '/settings/{-$tab}', params: {} })
            }}
          >
            {t('common.settings')}
          </StowMenuItem>
          <StowMenuItem
            icon="close"
            onClick={() => {
              close()
              signOut()
            }}
          >
            {t('common.sign_out')}
          </StowMenuItem>
        </>
      )}
    >
      <StowIconButton label={name}>
        <span className={styles.avatar}>{name.slice(0, 1).toUpperCase()}</span>
      </StowIconButton>
    </StowMenuButton>
  )
}
