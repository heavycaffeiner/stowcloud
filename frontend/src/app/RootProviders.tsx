import type { PropsWithChildren } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { queryClient } from '../lib/query/client'
import { localeStore } from '../lib/i18n/state'
import { useStore } from '../lib/store/use-store'
import { ui } from '../lib/store/ui.store'
import { useMduiBootstrap, useMduiLocale, useMduiTheme } from './shell/use-root-providers'
export function RootProviders({ children }: PropsWithChildren) {
  const theme = useStore(ui, (state) => state.theme)
  const locale = useStore(localeStore, (state) => state.locale)

  useMduiBootstrap()
  useMduiTheme(theme)
  useMduiLocale(locale)

  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
}
