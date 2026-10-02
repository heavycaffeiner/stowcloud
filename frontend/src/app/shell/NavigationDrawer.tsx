import { useState, type MouseEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { describeApiError } from '../../api/error-text'
import { useI18n } from '../../hooks/use-i18n'
import { useSession, type RootEntry } from '../../features/auth/api'
import { createMenuRequest } from '../../features/file-browser/create-menu'
import { useSetRootOrder } from '../../features/settings/api'
import { cx, Icon, StowButton, StowDrawer, StowIconButton, VirtualList } from '@/shared/ui'
import { useNavigation, type NavId, type NavItem } from './navigation'
import { sidebar } from './sidebar'
import { avatar } from './AppShell.css'
import * as styles from './NavigationDrawer.css'

const FILE_DESTINATIONS: readonly NavId[] = ['recent', 'trash', 'links']
const SETTING_DESTINATIONS: readonly NavId[] = ['settings', 'admin']

export interface NavigationDrawerProps {
  /** Rendered as a modal sheet over the compact layout instead of the wide sidebar. */
  readonly overlay?: boolean
  readonly onClose?: () => void
}

export function NavigationDrawer({ overlay = false, onClose }: NavigationDrawerProps) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const { items, active, roots } = useNavigation()
  const user = useSession().data?.user
  const collapsed = !overlay && sidebar.value === 'collapsed'

  const go = (href: string): void => {
    void navigate({ href })
    onClose?.()
  }
  const openCreateMenu = (event: MouseEvent<HTMLElement>): void => {
    // The sidebar sits on the left, so the menu anchors its left edge to the button.
    const rect = event.currentTarget.getBoundingClientRect()
    createMenuRequest.value = { x: rect.left, y: rect.bottom + 4, align: 'start' }
    onClose?.()
  }

  const content = (
    <>
      {overlay ? (
        <div className={styles.overlayHeader}>
          <StowIconButton label={t('common.close')} icon="close" onClick={onClose} />
          <span className={styles.appName}>Stowcloud</span>
          <span className={avatar} aria-hidden="true">
            {(user?.display_name || user?.name || 'S').slice(0, 1).toUpperCase()}
          </span>
        </div>
      ) : null}

      <div className={styles.body}>
        {active === 'files' ? (
          <div className={styles.newWrap}>
            {collapsed ? (
              <StowIconButton label={t('browse.new')} icon="add" onClick={openCreateMenu} />
            ) : (
              <StowButton
                variant="outlined"
                icon={<Icon name="add" size={20} />}
                className={styles.newBtn}
                onClick={openCreateMenu}
              >
                {t('browse.new')}
              </StowButton>
            )}
          </div>
        ) : null}

        {!collapsed ? <RootList overlay={overlay} onSelect={go} /> : null}

        {!collapsed && (active === 'files' || roots.length > 0) ? (
          <div className={styles.divider} role="separator" />
        ) : null}
        <Destinations
          items={items.filter((item) => FILE_DESTINATIONS.includes(item.id))}
          active={active}
          collapsed={collapsed}
          onSelect={go}
        />

        <div className={styles.divider} role="separator" />
        {!collapsed ? <div className={styles.sectionTitle}>{t('common.settings')}</div> : null}
        <Destinations
          items={items.filter((item) => SETTING_DESTINATIONS.includes(item.id))}
          active={active}
          collapsed={collapsed}
          onSelect={go}
        />
      </div>
    </>
  )

  if (!overlay) {
    return (
      <nav className={cx(styles.root, collapsed && styles.collapsed)} aria-label={t('common.main_menu')}>
        {content}
      </nav>
    )
  }
  return (
    <StowDrawer
      open
      id="sc-shell-drawer"
      label={t('common.main_menu')}
      className={styles.overlay}
      onClose={() => onClose?.()}
    >
      {content}
    </StowDrawer>
  )
}

function Destinations({
  items,
  active,
  collapsed,
  onSelect
}: {
  readonly items: readonly NavItem[]
  readonly active: NavId
  readonly collapsed: boolean
  readonly onSelect: (href: string) => void
}) {
  return (
    <ul className={styles.list}>
      {items.map((item) => (
        <li key={item.id} className={styles.entry}>
          <button
            type="button"
            className={cx(styles.item, active === item.id && styles.itemActive)}
            aria-current={active === item.id ? 'page' : undefined}
            aria-label={item.label}
            title={collapsed ? item.label : undefined}
            onClick={() => onSelect(item.href)}
          >
            <span className={styles.itemIcon}>
              <Icon name={item.icon} size={20} />
            </span>
            {!collapsed ? <span className={styles.itemLabel}>{item.label}</span> : null}
          </button>
        </li>
      ))}
    </ul>
  )
}

/** The user's roots, reorderable. A saved order shows at once and holds until the session reports it back. */
function RootList({ overlay, onSelect }: { readonly overlay: boolean; readonly onSelect: (href: string) => void }) {
  const { t } = useI18n()
  const { roots, activeRoot } = useNavigation()
  const orderMutation = useSetRootOrder()
  const [reordering, setReordering] = useState(false)
  const [pending, setPending] = useState<{ base: readonly RootEntry[]; order: readonly RootEntry[] } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const displayRoots = pending?.base === roots ? pending.order : roots
  if (displayRoots.length === 0) return null

  const moveRoot = (index: number, direction: -1 | 1): void => {
    const target = index + direction
    if (target < 0 || target >= displayRoots.length) return
    const next = displayRoots.with(index, displayRoots[target]).with(target, displayRoots[index])
    setPending({ base: roots, order: next })
    setError(null)
    orderMutation.mutate(
      next.map((root) => root.label),
      {
        onError: (failure) => {
          setPending(null)
          setError(describeApiError(failure, t('nav.could_not_save_order')))
        },
        onSettled: () => setReordering(false)
      }
    )
  }

  return (
    <div>
      <div className={styles.sectionHeader}>
        <span className={styles.sectionTitle}>{t('nav.folders')}</span>
        <StowButton variant="text" onClick={() => setReordering((current) => !current)}>
          {reordering ? t('nav.reorder_done') : t('nav.reorder')}
        </StowButton>
      </div>
      <ul className={styles.list} aria-label={t('nav.folder_selector')}>
        <li className={styles.entry}>
          <VirtualList
            className={styles.sublist}
            items={displayRoots}
            itemKey={(root) => root.label}
            estimateSize={reordering || overlay ? 48 : 40}
            renderItem={(root, index) =>
              reordering ? (
                <div className={styles.reorderRow}>
                  <span className={styles.itemIcon}>
                    <Icon name="folder" size={18} />
                  </span>
                  <span className={styles.itemLabel}>{root.label}</span>
                  <span className={styles.reorderActions}>
                    <StowIconButton
                      label={t('nav.move_up', { name: root.label })}
                      disabled={index === 0}
                      onClick={() => moveRoot(index, -1)}
                    >
                      <span className={cx(styles.reorderChevron, styles.reorderChevronUp)}>
                        <Icon name="chevron_right" size={16} />
                      </span>
                    </StowIconButton>
                    <StowIconButton
                      label={t('nav.move_down', { name: root.label })}
                      disabled={index === displayRoots.length - 1}
                      onClick={() => moveRoot(index, 1)}
                    >
                      <span className={cx(styles.reorderChevron, styles.reorderChevronDown)}>
                        <Icon name="chevron_right" size={16} />
                      </span>
                    </StowIconButton>
                  </span>
                </div>
              ) : (
                <button
                  type="button"
                  className={cx(styles.item, activeRoot === root.label && styles.itemActive)}
                  aria-current={activeRoot === root.label ? 'location' : undefined}
                  onClick={() => onSelect(`/b/${encodeURIComponent(root.label)}`)}
                >
                  <span className={styles.itemIcon}>
                    <Icon name="folder" size={18} />
                  </span>
                  <span className={styles.itemLabel}>{root.label}</span>
                </button>
              )
            }
          />
          {error ? (
            <p className={styles.reorderError} role="alert">
              {error}
            </p>
          ) : null}
        </li>
      </ul>
    </div>
  )
}
