import { useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useHotkeys } from 'react-hotkeys-hook'
import { useBooleanState } from 'react-simplikit'
import { useI18n } from '../../hooks/use-i18n'
import { useOutsideDismiss } from '../../hooks/use-outside-dismiss'
import { useRestoreFocus } from '../../hooks/use-restore-focus'
import { useSession } from '../../features/auth/api'
import { useOpenSearch } from '../../features/search/state'
import { useSignOut } from '../../features/settings/hooks/use-sign-out'
import { Icon } from '../../ui/Icon'
import { useCompact } from '../../ui/use-compact'
import { useCurrentFolder, useNavigation } from './navigation'
import { sidebar, toggleSidebar } from './sidebar'
import * as styles from './AppShell.css'
import * as iconButtonStyles from '../../ui/IconButton.css'
import * as utilitiesStyles from '../../ui/utilities.css'
import { cx } from '../../ui/cx'

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
          <button
            type="button"
            className={cx(styles.headerMenuBtn, iconButtonStyles.root)}
            aria-label={t('nav.toggle_sidebar')}
            aria-expanded={sidebar.value === 'expanded'}
            onClick={toggleSidebar}
          >
            <Icon name="menu" size={22} />
          </button>
        ) : null}
        <button type="button" className={styles.headerBrandBtn} onClick={() => void navigate(files.href)}>
          <span className={styles.headerBrand}>Stowcloud</span>
        </button>
      </div>

      <div className={styles.headerCenter}>
        <SearchLauncher />
      </div>

      <div className={styles.headerRight}>
        {!compact ? (
          <>
            <button
              type="button"
              className={cx(styles.headerIconBtn, iconButtonStyles.root)}
              aria-label={t('nav.help')}
              onClick={() => window.open(REPOSITORY, '_blank', 'noopener,noreferrer')}
            >
              <Icon name="help" size={20} />
            </button>
            <button
              type="button"
              className={cx(styles.headerIconBtn, iconButtonStyles.root)}
              aria-label={t('common.settings')}
              onClick={() => void navigate('/settings')}
            >
              <Icon name="settings" size={20} />
            </button>
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
    <button
      className={cx(styles.headerSearch, utilitiesStyles.focusRing)}
      type="button"
      onClick={open}
      aria-label={t('common.search')}
    >
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
  const [open, , close, toggle] = useBooleanState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const name = user?.display_name || user?.name
  useOutsideDismiss(open, wrapRef, close)
  useRestoreFocus(open)
  return (
    <div className={styles.headerAccountWrap} ref={wrapRef}>
      <button
        type="button"
        className={styles.headerAvatarBtn}
        aria-label={name || 'User'}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={toggle}
      >
        <span className={styles.headerAvatar}>{(name || 'S').slice(0, 1).toUpperCase()}</span>
      </button>
      {open ? (
        <div className={styles.headerAccountMenu} role="menu">
          <div className={styles.headerAccountName}>{name}</div>
          <button
            type="button"
            role="menuitem"
            className={styles.headerAccountItem}
            onClick={() => {
              close()
              void navigate('/settings')
            }}
          >
            <Icon name="settings" size={18} />
            {t('common.settings')}
          </button>
          <button
            type="button"
            role="menuitem"
            className={styles.headerAccountItem}
            onClick={() => {
              close()
              signOut()
            }}
          >
            <Icon name="close" size={18} />
            {t('common.sign_out')}
          </button>
        </div>
      ) : null}
    </div>
  )
}
