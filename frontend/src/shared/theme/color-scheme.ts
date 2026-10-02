import type { MantineColorScheme, MantineColorSchemeManager } from '@mantine/core'
import type { Signal } from '@preact/signals-react'

export type ThemePref = 'system' | 'light' | 'dark'

const toScheme = (pref: ThemePref): MantineColorScheme => (pref === 'system' ? 'auto' : pref)

/** Lets Mantine follow and update a theme preference signal instead of keeping its own storage key. */
export function colorSchemeManager(pref: Signal<ThemePref>): MantineColorSchemeManager {
  let stop: (() => void) | undefined
  return {
    get: () => toScheme(pref.peek()),
    set: (scheme) => {
      pref.value = scheme === 'auto' ? 'system' : scheme
    },
    subscribe: (onUpdate) => {
      stop?.()
      stop = pref.subscribe((value) => onUpdate(toScheme(value)))
    },
    unsubscribe: () => {
      stop?.()
      stop = undefined
    },
    clear: () => {
      pref.value = 'system'
    }
  }
}

/** Sets the scheme attribute before React mounts, so an explicit preference never paints in the other scheme. */
export function applyColorSchemeAttribute(pref: ThemePref): void {
  if (pref !== 'system') document.documentElement.setAttribute('data-mantine-color-scheme', pref)
}
