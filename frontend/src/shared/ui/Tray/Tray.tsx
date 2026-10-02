import '@mantine/core/styles/UnstyledButton.layer.css'
import { UnstyledButton } from '@mantine/core'
import type { ReactNode } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { Icon, type IconName } from '../Icon'
import { StowIconButton } from '../IconButton'
import { VirtualList } from '../VirtualList'
import * as styles from './Tray.css'

export interface StowTrayProps<T> {
  /** Heads the tray and names it for assistive technology. */
  title: string
  icon: IconName
  /** A short count or state after the title. */
  status: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onClearFinished: () => void
  /** Shown between the header and the list, such as a lost connection. */
  notice?: ReactNode
  items: readonly T[]
  itemKey: (item: T) => string
  estimateSize: number
  renderItem: (item: T) => ReactNode
}

/** A floating panel of background work, such as uploads or server jobs. The header folds the list away. */
export function StowTray<T>({
  title,
  icon,
  status,
  open,
  onOpenChange,
  onClearFinished,
  notice,
  items,
  itemKey,
  estimateSize,
  renderItem
}: StowTrayProps<T>) {
  const { t } = useI18n()
  return (
    <section className={styles.root} aria-label={title}>
      <header className={styles.header}>
        <UnstyledButton className={styles.title} onClick={() => onOpenChange(!open)} aria-expanded={open}>
          <Icon name={icon} />
          <span>{title}</span>
          <span>{status}</span>
        </UnstyledButton>
        <div className={styles.controls}>
          <StowIconButton label={t('common.clear_finished_items')} onClick={onClearFinished} icon="check" />
          <StowIconButton
            label={open ? t('common.collapse') : t('common.expand')}
            expanded={open}
            onClick={() => onOpenChange(!open)}
          >
            <Icon name={open ? 'chevron_right' : 'chevron_left'} />
          </StowIconButton>
        </div>
      </header>
      {notice ? <p className={styles.notice}>{notice}</p> : null}
      {open ? (
        <div className={styles.scroll}>
          <VirtualList
            className={styles.list}
            items={items}
            itemKey={itemKey}
            estimateSize={estimateSize}
            itemProps={() => ({ className: styles.item })}
            renderItem={renderItem}
          />
        </div>
      ) : null}
    </section>
  )
}
