import { QueryClientProvider } from '@tanstack/react-query'
import type { PropsWithChildren } from 'react'
import { I18nextProvider, useTranslation } from 'react-i18next'
import { queryClient } from '../lib/query/client'
import { i18n } from '../lib/i18n/state'
import { useStore } from '../lib/store/use-store'
import { ui } from '../lib/store/ui.store'
import { useMduiBootstrap, useMduiLocale, useMduiTheme } from './shell/use-root-providers'

export function RootProviders({ children }: PropsWithChildren) {
  const theme = useStore(ui, (state) => state.theme)
  const { i18n: translation } = useTranslation(undefined, { i18n })
  const locale = translation.language === 'en' ? 'en' : 'ko'

  useMduiBootstrap()
  useMduiTheme(theme)
  useMduiLocale(locale)

  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  )
}
