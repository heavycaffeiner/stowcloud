import { QueryClientProvider } from '@tanstack/react-query'
import type { PropsWithChildren } from 'react'
import { I18nextProvider, useTranslation } from 'react-i18next'
import { queryClient } from '../lib/query/client'
import { i18n } from '../lib/i18n/state'
import { useMduiLocale } from './use-root-providers'

export function RootProviders({ children }: PropsWithChildren) {
  const { i18n: translation } = useTranslation(undefined, { i18n })
  useMduiLocale(translation.language === 'en' ? 'en' : 'ko')

  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </I18nextProvider>
  )
}
