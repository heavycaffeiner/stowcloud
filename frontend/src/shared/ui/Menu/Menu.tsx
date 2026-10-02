import '@mantine/core/styles/Popover.layer.css'
import '@mantine/core/styles/Menu.layer.css'
import { createScopedKeydownHandler, Menu, UnstyledButton } from '@mantine/core'
import {
  cloneElement,
  createContext,
  use,
  useId,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
  type Ref
} from 'react'
import { overlay } from 'overlay-kit'
import { media } from '@/shared/theme'
import { useMediaQuery } from '../../../hooks/use-media-query'
import { cx } from '../cx'
import { StowDrawer } from '../Drawer'
import { Icon, type IconName } from '../Icon'
import * as styles from './Menu.css'

type Align = 'start' | 'end'

// Items in the compact sheet sit outside Mantine's menu, so there they render as plain buttons.
const InSheet = createContext(false)

const checkIcon = <Icon name="check" size={18} />

// The owner sets the trigger's attributes, since the sheet has to report them too.
// Menu forwards this to Popover at runtime but leaves it out of its types.
const ownTriggerRoles = { withRoles: false }

const sheetKeys = createScopedKeydownHandler({
  siblingSelector: '[data-menu-item]',
  parentSelector: '[data-menu-sheet]',
  activateOnFocus: false,
  loop: true,
  orientation: 'vertical'
})

interface MenuSurfaceProps {
  open: boolean
  /** The menu's accessible name. */
  label: string
  id?: string
  align: Align
  /** The element the popup lines up with. */
  target: ReactElement
  offset: number
  /** True when the target asks to open; false for Escape or a press outside. */
  onChange: (open: boolean) => void
  onClosed?: () => void
  children: ReactNode
}

/** A popup by its target on the wide layout, and a bottom sheet on the narrower ones. */
function MenuSurface({ open, label, id, align, target, offset, onChange, onClosed, children }: MenuSurfaceProps) {
  const sheet = useMediaQuery(media.notWide)
  return (
    <>
      <Menu
        opened={open && !sheet}
        onChange={onChange}
        onExitTransitionEnd={onClosed}
        position={align === 'end' ? 'bottom-end' : 'bottom-start'}
        offset={offset}
        {...ownTriggerRoles}
        // Closing focuses the trigger at once; Mantine's delayed return would pull focus out of a dialog an item opens.
        returnFocus={false}
        closeOnItemClick={false}
        withInitialFocusPlaceholder={false}
        checkIcon={checkIcon}
        classNames={{ item: styles.item, itemLabel: styles.label, itemIndicator: styles.indicator }}
      >
        <Menu.Target>{target}</Menu.Target>
        <Menu.Dropdown id={id} aria-label={label} className={styles.dropdown}>
          {children}
        </Menu.Dropdown>
      </Menu>
      {sheet ? (
        <StowDrawer open={open} label={label} position="bottom" onClose={() => onChange(false)} onClosed={onClosed}>
          <div className={styles.sheetHandle} aria-hidden="true" />
          <div id={id} role="menu" aria-label={label} className={styles.sheetList} data-menu-sheet>
            <InSheet value>{children}</InSheet>
          </div>
        </StowDrawer>
      ) : null}
    </>
  )
}

/** Where a menu opens. `align` says which edge of the menu sits at `x`; `trigger` gets focus back on close. */
export interface MenuAnchor {
  readonly x: number
  readonly y: number
  readonly align?: Align
  readonly trigger?: HTMLElement | null
}

/** Opens a menu at a point rather than under a button, such as a context menu. */
export function openMenu(at: MenuAnchor, label: string, content: (close: () => void) => ReactNode): void {
  overlay.open(({ isOpen, close, unmount }) => {
    const done = (): void => {
      close()
      at.trigger?.focus()
    }
    return (
      <MenuSurface
        open={isOpen}
        label={label}
        align={at.align ?? 'start'}
        offset={0}
        target={<span className={styles.anchor} style={{ left: at.x, top: at.y }} />}
        onChange={(next) => {
          if (!next) done()
        }}
        onClosed={unmount}
      >
        {content(done)}
      </MenuSurface>
    )
  })
}

interface TriggerProps {
  ref?: Ref<HTMLElement>
  'aria-haspopup'?: 'menu'
  'aria-expanded'?: boolean
  'aria-controls'?: string
}

export interface StowMenuButtonProps {
  /** The menu's accessible name. */
  label: string
  /** Which edge of the trigger the menu lines up with. */
  align?: Align
  /** The trigger. It receives the ref and the menu button attributes. */
  children: ReactElement<TriggerProps>
  menu: (close: () => void) => ReactNode
}

/** A trigger that owns its menu: open state, position and focus return. */
export function StowMenuButton({ label, align = 'start', children, menu }: StowMenuButtonProps) {
  const [open, setOpen] = useState(false)
  const id = useId()
  const trigger = useRef<HTMLElement>(null)
  const close = (): void => {
    setOpen(false)
    trigger.current?.focus()
  }
  return (
    <MenuSurface
      open={open}
      label={label}
      id={id}
      align={align}
      offset={4}
      target={cloneElement(children, {
        ref: trigger,
        'aria-haspopup': 'menu',
        'aria-expanded': open,
        'aria-controls': open ? id : undefined
      })}
      onChange={(next) => (next ? setOpen(true) : close())}
    >
      {menu(close)}
    </MenuSurface>
  )
}

/** A heading inside a menu, such as whose account it is. Keyboard focus passes over it. */
export function StowMenuLabel({ children }: { children: ReactNode }) {
  return use(InSheet) ? (
    <div className={cx(Menu.classes.label, styles.heading)}>{children}</div>
  ) : (
    <Menu.Label className={styles.heading}>{children}</Menu.Label>
  )
}

export interface StowMenuItemProps {
  /** Makes the item one choice of a radio group, checked or not. */
  checked?: boolean
  icon?: IconName
  title?: string
  className?: string
  onClick?: () => void
  children?: ReactNode
}

export function StowMenuItem({ checked, icon, title, className, onClick, children }: StowMenuItemProps) {
  const inSheet = use(InSheet)
  const leading = icon ? <Icon name={icon} size={20} className={styles.icon} /> : undefined
  if (inSheet) {
    return (
      <UnstyledButton
        role={checked === undefined ? 'menuitem' : 'menuitemradio'}
        aria-checked={checked}
        title={title}
        className={cx(Menu.classes.item, styles.item, styles.sheetItem, className)}
        data-menu-item
        onClick={onClick}
        onKeyDown={sheetKeys}
      >
        {checked === undefined ? null : (
          <span className={cx(Menu.classes.itemIndicator, styles.indicator)}>{checked ? checkIcon : null}</span>
        )}
        {leading ? (
          <span className={Menu.classes.itemSection} data-position="left">
            {leading}
          </span>
        ) : null}
        <span className={cx(Menu.classes.itemLabel, styles.label)}>{children}</span>
      </UnstyledButton>
    )
  }
  if (checked !== undefined) {
    return (
      // The item is not in a Menu.RadioGroup, so `checked` decides and the group value goes unused.
      <Menu.RadioItem value="" checked={checked} title={title} className={className} onClick={onClick}>
        {children}
      </Menu.RadioItem>
    )
  }
  return (
    <Menu.Item leftSection={leading} title={title} className={className} onClick={onClick}>
      {children}
    </Menu.Item>
  )
}
