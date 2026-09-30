import { cx } from './cx'
import { icons, type IconName } from './icons'
import * as styles from './Icon.css'

export interface IconProps {
  name: string
  size?: number
  className?: string
  slot?: string
}

/** Render a shipped Material Symbols icon as inline SVG. */
export function Icon({ name, size = 20, className, slot }: IconProps) {
  const icon = icons[name as IconName]
  if (!icon) return null
  const width = icon.width ?? 24
  const height = icon.height ?? 24
  return (
    <svg
      className={cx(styles.root, className)}
      slot={slot}
      width={size}
      height={size}
      viewBox={`0 0 ${width} ${height}`}
      aria-hidden="true"
      focusable="false"
      dangerouslySetInnerHTML={{ __html: icon.body }}
    />
  )
}
