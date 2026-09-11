export interface DividerProps {
  inset?: boolean
  vertical?: boolean
  middle?: boolean
}

export function Divider({ inset = false, vertical = false, middle = false }: DividerProps) {
  return <mdui-divider inset={inset} vertical={vertical} middle={middle} />
}
