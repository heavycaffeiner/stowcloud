import { icons, type IconName } from './icons'

export interface IconProps {
  name: IconName
  size?: number
  className?: string
}

/** A shipped Material Symbols icon as inline SVG. Decorative: the control around it carries the name. */
export function Icon({ name, size = 20, className }: IconProps) {
  const icon = icons[name]
  const width = icon.width ?? 24
  const height = icon.height ?? 24
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox={`0 0 ${width} ${height}`}
      aria-hidden="true"
      focusable="false"
      dangerouslySetInnerHTML={{ __html: icon.body }}
    />
  )
}
