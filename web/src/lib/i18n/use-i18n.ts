import { useStore } from '../store/use-store'
import { localeStore } from './state'
import { t, tp } from './index'

export function useI18n() {
  useStore(localeStore, (state) => state.locale)
  return { t, tp }
}
