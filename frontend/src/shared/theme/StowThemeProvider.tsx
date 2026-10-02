import { MantineProvider } from '@mantine/core'
import type { Signal } from '@preact/signals-react'
import { useState, type PropsWithChildren } from 'react'
import { colorSchemeManager, type ThemePref } from './color-scheme'
import { cssVariablesResolver, mantineTheme } from './mantine'

/** Puts the Stowcloud theme under Mantine and keeps the color scheme in step with the stored preference. */
export function StowThemeProvider({ pref, children }: PropsWithChildren<{ pref: Signal<ThemePref> }>) {
  const [manager] = useState(() => colorSchemeManager(pref))
  return (
    <MantineProvider
      theme={mantineTheme}
      cssVariablesResolver={cssVariablesResolver}
      colorSchemeManager={manager}
      defaultColorScheme="auto"
    >
      {children}
    </MantineProvider>
  )
}
