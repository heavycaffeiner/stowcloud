import 'mdui/components/segmented-button-group.js'
import 'mdui/components/segmented-button.js'
import type { ReactNode } from 'react'
import { useLayoutEffect, useReducer, useRef } from 'react'

export interface SegmentedOption {
  value: string
  label: ReactNode
}

export interface SegmentedControlProps {
  label: string
  value: string
  options: SegmentedOption[]
  onChange: (value: string) => void
}

interface SegmentedGroupElement extends HTMLElement {
  value: string | string[]
}

/** A single-choice segmented control that always shows the owner's value. */
export function SegmentedControl({ label, value, options, onChange }: SegmentedControlProps) {
  const ref = useRef<SegmentedGroupElement>(null)
  const [changes, noteChange] = useReducer((count: number) => count + 1, 0)

  // mdui moves the selection before the owner decides; a refused, pending or failed change snaps back here.
  useLayoutEffect(() => {
    const element = ref.current
    if (element && element.value !== value) element.value = value
  }, [value, changes])

  return (
    <mdui-segmented-button-group
      ref={ref}
      selects="single"
      aria-label={label}
      value={value}
      onChange={() => {
        const next = ref.current?.value
        if (typeof next !== 'string' || next === value) return
        // An empty value means the selected segment was clicked again; a single choice cannot be cleared.
        if (next !== '') onChange(next)
        noteChange()
      }}
    >
      {options.map((option) => (
        <mdui-segmented-button key={option.value} value={option.value}>
          {option.label}
        </mdui-segmented-button>
      ))}
    </mdui-segmented-button-group>
  )
}
