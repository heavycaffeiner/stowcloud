import { QueryClientProvider } from '@tanstack/react-query'
import type { PropsWithChildren } from 'react'
import { I18nextProvider, useTranslation } from 'react-i18next'
import { StowThemeProvider } from '@/shared/theme/StowThemeProvider'
import { queryClient } from '../api/query-client'
import { theme } from '../features/settings/theme'
import { i18n } from '../i18n/state'
import { useDocumentLanguage } from './use-root-providers'

export function RootProviders({ children }: PropsWithChildren) {
  const { i18n: translation } = useTranslation(undefined, { i18n })
  useDocumentLanguage(translation.language === 'en' ? 'en' : 'ko')

  return (
    <I18nextProvider i18n={i18n}>
      <StowThemeProvider pref={theme}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </StowThemeProvider>
    </I18nextProvider>
  )
}
