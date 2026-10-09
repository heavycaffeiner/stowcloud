import { useI18n } from '../../../hooks/use-i18n'
import { cx, Icon } from '@/shared/ui'
import { focusRing } from '@/shared/theme'
import * as styles from './ParentFolderItem.css'

/** Navigation stays outside the file entries, so it cannot be selected or modified. */
export function ParentFolderItem({ onNavigate, className }: { onNavigate: () => void; className?: string }) {
  const { t } = useI18n()
  return (
    <button
      type="button"
      className={cx(styles.root, focusRing, className)}
      aria-label={t('browse.parent_folder')}
      onClick={onNavigate}
      onContextMenu={(event) => event.stopPropagation()}
    >
      <Icon name="folder" size={20} />
      <span aria-hidden="true">..</span>
      <span className={styles.label}>{t('browse.parent_folder')}</span>
    </button>
  )
}
