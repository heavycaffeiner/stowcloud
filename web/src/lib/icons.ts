// Every icon the app uses, in one place.
//
// These come from Material Symbols (shipped with m3-svelte, so no extra
// dependency) rather than the 33 hand-drawn SVG paths this file replaced.
// Each icon is its own module, so the bundler drops the ~36k we don't touch —
// the initial-JS budget in still holds.
//
// The keyed record exists because several call sites pick an icon at runtime
// (`icons[iconName(entry)]`); import the named export directly when the icon
// is known statically, so only that one is bundled.
import type { IconifyIcon } from '@iconify/types'
import iconAccountTree from '@ktibow/iconset-material-symbols/account-tree-outline'
import iconAdd from '@ktibow/iconset-material-symbols/add'
import iconAdmin from '@ktibow/iconset-material-symbols/admin-panel-settings-outline'
import iconCheck from '@ktibow/iconset-material-symbols/check'
import iconChevronLeft from '@ktibow/iconset-material-symbols/chevron-left'
import iconChevronRight from '@ktibow/iconset-material-symbols/chevron-right'
import iconClose from '@ktibow/iconset-material-symbols/close'
import iconCopy from '@ktibow/iconset-material-symbols/content-copy-outline'
import iconDelete from '@ktibow/iconset-material-symbols/delete-outline'
import iconDownload from '@ktibow/iconset-material-symbols/download'
import iconEditDocument from '@ktibow/iconset-material-symbols/edit-document-outline'
import iconFile from '@ktibow/iconset-material-symbols/draft-outline'
import iconFolder from '@ktibow/iconset-material-symbols/folder-outline'
import iconGrid from '@ktibow/iconset-material-symbols/grid-view-outline'
import iconHome from '@ktibow/iconset-material-symbols/home-outline'
import iconImage from '@ktibow/iconset-material-symbols/image-outline'
import iconInfo from '@ktibow/iconset-material-symbols/info-outline'
import iconLink from '@ktibow/iconset-material-symbols/link'
import iconList from '@ktibow/iconset-material-symbols/format-list-bulleted'
import iconLock from '@ktibow/iconset-material-symbols/lock-outline'
import iconMenu from '@ktibow/iconset-material-symbols/menu'
import iconMore from '@ktibow/iconset-material-symbols/more-vert'
import iconMove from '@ktibow/iconset-material-symbols/drive-file-move-outline'
import iconRecent from '@ktibow/iconset-material-symbols/history'
import iconRefresh from '@ktibow/iconset-material-symbols/refresh'
import iconRename from '@ktibow/iconset-material-symbols/edit-outline'
import iconRestore from '@ktibow/iconset-material-symbols/restore-from-trash-outline'
import iconSearch from '@ktibow/iconset-material-symbols/search'
import iconSettings from '@ktibow/iconset-material-symbols/settings-outline'
import iconTrash from '@ktibow/iconset-material-symbols/delete-sweep-outline'
import iconUpload from '@ktibow/iconset-material-symbols/upload-file-outline'
import iconUploadFolder from '@ktibow/iconset-material-symbols/drive-folder-upload-outline'
import iconWarning from '@ktibow/iconset-material-symbols/warning-outline'

export {
  iconAccountTree,
  iconAdd,
  iconAdmin,
  iconCheck,
  iconChevronLeft,
  iconChevronRight,
  iconClose,
  iconCopy,
  iconDelete,
  iconDownload,
  iconFile,
  iconFolder,
  iconGrid,
  iconHome,
  iconImage,
  iconLink,
  iconList,
  iconLock,
  iconMenu,
  iconMore,
  iconMove,
  iconRecent,
  iconRefresh,
  iconRename,
  iconRestore,
  iconSearch,
  iconSettings,
  iconTrash,
  iconUpload,
  iconUploadFolder,
  iconWarning
}

/** Runtime lookup. Keys are the names the old inline icon set used. */
export const icons = {
  add: iconAdd,
  admin: iconAdmin,
  check: iconCheck,
  'chevron-left': iconChevronLeft,
  'chevron-right': iconChevronRight,
  close: iconClose,
  copy: iconCopy,
  delete: iconDelete,
  download: iconDownload,
  'edit-document': iconEditDocument,
  file: iconFile,
  folder: iconFolder,
  'folder-tree': iconAccountTree,
  grid: iconGrid,
  home: iconHome,
  image: iconImage,
  info: iconInfo,
  link: iconLink,
  list: iconList,
  lock: iconLock,
  menu: iconMenu,
  'more-vert': iconMore,
  move: iconMove,
  recent: iconRecent,
  refresh: iconRefresh,
  rename: iconRename,
  restore: iconRestore,
  search: iconSearch,
  settings: iconSettings,
  trash: iconTrash,
  upload: iconUpload,
  'upload-folder': iconUploadFolder,
  warning: iconWarning
} satisfies Record<string, IconifyIcon>

export type IconName = keyof typeof icons
