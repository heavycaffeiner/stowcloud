import { useRouteStore } from '../../use-route-store'
import type { SetupFinding } from '../../../lib/api/setup'

export type SetupState = {
  token: string
  username: string
  password: string
  passwordConfirm: string
  appHosts: string
  trustedProxies: string
  shareName: string
  sharePath: string
  accountCreated: boolean
  doneButLoginFailed: boolean
  shareFailed: boolean
  shareRetryError: string | null
  errorMessage: string | null
  warnings: SetupFinding[]
  pickerOpen: boolean
  pickerAuthenticated: boolean
  step: number
}

export type SetupActions = {
  patch: (patch: Partial<SetupState> | ((state: SetupState) => Partial<SetupState>)) => void
  setToken: (value: string) => void
  setUsername: (value: string) => void
  setPassword: (value: string) => void
  setPasswordConfirm: (value: string) => void
  setAppHosts: (value: string) => void
  setTrustedProxies: (value: string) => void
  setShareName: (value: string) => void
  setSharePath: (value: string) => void
  nextStep: () => void
  previousStep: () => void
}

const initialState: SetupState = {
  token: '', username: '', password: '', passwordConfirm: '',
  appHosts: typeof location === 'undefined' ? '' : location.hostname,
  trustedProxies: '', shareName: '', sharePath: '', accountCreated: false,
  doneButLoginFailed: false, shareFailed: false, shareRetryError: null,
  errorMessage: null, warnings: [], pickerOpen: false, pickerAuthenticated: false, step: 1
}

export function useSetupState(): { state: SetupState; actions: SetupActions } {
  const [state, patch] = useRouteStore<SetupState>(initialState)
  const actions: SetupActions = {
    patch,
    setToken: (value) => patch({ token: value }),
    setUsername: (value) => patch({ username: value }),
    setPassword: (value) => patch({ password: value }),
    setPasswordConfirm: (value) => patch({ passwordConfirm: value }),
    setAppHosts: (value) => patch({ appHosts: value }),
    setTrustedProxies: (value) => patch({ trustedProxies: value }),
    setShareName: (value) => patch({ shareName: value }),
    setSharePath: (value) => patch({ sharePath: value }),
    nextStep: () => patch((current) => ({ step: Math.min(current.step + 1, 3) })),
    previousStep: () => patch((current) => ({ step: Math.max(current.step - 1, 1) }))
  }
  return { state, actions }
}
