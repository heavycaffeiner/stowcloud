import { useLocation, useMatch, useRouter } from '@tanstack/react-router'
import { useSession } from '../../features/auth/api'
import { splatOf, splatPath } from '../../features/files/browse-search'
import { lastFolder } from '../../features/files/location'
import { useI18n } from '../../hooks/use-i18n'

export type NavId = 'files' | 'recent' | 'trash' | 'links' | 'settings' | 'admin'

export interface NavItem {
  readonly id: NavId
  readonly label: string
  readonly icon: string
  readonly href: string
}

const rootOf = (path: string): string => path.split('/').filter(Boolean)[0] ?? ''

/** The folder the file browser shows, or null on any other page. */
function useFolderOnScreen(): string | null {
  return useMatch({ from: '/_app/b/$', shouldThrow: false, select: (match) => splatPath(match.params._splat) }) ?? null
}

/** The folder on screen, or the one the browser last showed. */
export function useCurrentFolder(): string {
  return useFolderOnScreen() ?? lastFolder.value
}

/**
 * The shell's destinations and which one is current. Files leads back to the
 * folder the user left while its root is still listed, else to the first root.
 */
export function useNavigation() {
  const { t } = useI18n()
  const router = useRouter()
  const pathname = useLocation({ select: (location) => location.pathname })
  const onScreen = useFolderOnScreen()
  const folder = onScreen ?? lastFolder.value
  const session = useSession().data
  const roots = session?.roots ?? []
  const filesPath =
    folder === '/' || roots.some((root) => root.label === rootOf(folder))
      ? folder
      : roots.length > 0
        ? `/${roots[0].label}`
        : '/'
  const items: readonly NavItem[] = [
    {
      id: 'files',
      label: t('nav.files'),
      icon: 'folder',
      href: router.buildLocation({ to: '/b/$', params: splatOf(filesPath) }).href
    },
    { id: 'recent', label: t('nav.recent'), icon: 'history', href: '/recent' },
    { id: 'trash', label: t('common.trash'), icon: 'delete', href: '/trash' },
    { id: 'links', label: t('nav.links'), icon: 'link', href: '/links' },
    { id: 'settings', label: t('common.settings'), icon: 'settings', href: '/settings' },
    ...(session?.user.is_admin
      ? [{ id: 'admin', label: t('nav.admin'), icon: 'admin_panel_settings', href: '/admin' } as const]
      : [])
  ]
  const active = items.find((item) => item.id !== 'files' && pathname.startsWith(item.href))?.id ?? 'files'
  return { items, active, activeRoot: rootOf(onScreen ?? ''), roots }
}
