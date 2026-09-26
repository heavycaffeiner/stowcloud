import { useTranslation } from 'react-i18next'
import { i18n } from './state'
import { t, tp } from './index'

export function useI18n() {
  useTranslation(undefined, { i18n })
  return { t, tp }
}
