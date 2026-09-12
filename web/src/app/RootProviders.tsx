import { QueryClientProvider } from '@tanstack/react-query'
import { useEffect } from 'react'
import type { PropsWithChildren } from 'react'
import { localeTag } from '../lib/i18n'
import { localeStore } from '../lib/i18n/state'
import { queryClient } from '../lib/query/client'
import { useStore } from '../lib/store/use-store'
import { ui } from '../lib/store/ui.store'
import { applyMduiLocale, applyMduiTheme, initMdui } from '../lib/ui/mdui-runtime'

export function RootProviders({ children }: PropsWithChildren) {
  const theme = useStore(ui, (state) => state.theme)
  const locale = useStore(localeStore, (state) => state.locale)

  useEffect(() => {
    initMdui()
  }, [])

  useEffect(() => {
    applyMduiTheme(theme)
  }, [theme])

  useEffect(() => {
    document.documentElement.lang = localeTag()
    void applyMduiLocale(locale)
  }, [locale])

  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
}
