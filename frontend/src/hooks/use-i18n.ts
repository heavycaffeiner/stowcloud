import { useTranslation } from 'react-i18next'
import { i18n } from '../i18n/state'
import { t, tp } from '../i18n/index'

export function useI18n() {
  useTranslation(undefined, { i18n })
  return { t, tp }
}
