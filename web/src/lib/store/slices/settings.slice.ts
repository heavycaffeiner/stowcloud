import { createStore, type StoreApi } from 'zustand/vanilla'
import type { Result } from '../core/fp'
import { err, ok } from '../core/fp'

export type ModalStatus = 'idle' | 'submitting' | 'error' | 'success'

export interface ModalSnapshot<T> {
  readonly isOpen: boolean
  readonly status: ModalStatus
  readonly data: T
  readonly error: string | null
}

export type ModalAction<T> =
  | { type: 'OPEN'; data?: T }
  | { type: 'CLOSE' }
  | { type: 'SUBMIT' }
  | { type: 'SET_DATA'; patch: Partial<T> }
  | { type: 'FAIL'; error: string }
  | { type: 'SUCCEED' }

export function modalReducer<T>(state: ModalSnapshot<T>, action: ModalAction<T>): ModalSnapshot<T> {
  switch (action.type) {
    case 'OPEN':
      return {
        isOpen: true,
        status: 'idle',
        data: action.data !== undefined ? action.data : state.data,
        error: null
      }
    case 'CLOSE':
      return {
        ...state,
        isOpen: false,
        status: 'idle',
        error: null
      }
    case 'SUBMIT':
      return {
        ...state,
        status: 'submitting',
        error: null
      }
    case 'SET_DATA':
      return {
        ...state,
        data: { ...state.data, ...action.patch }
      }
    case 'FAIL':
      return {
        ...state,
        status: 'error',
        error: action.error
      }
    case 'SUCCEED':
      return {
        ...state,
        status: 'success',
        isOpen: false,
        error: null
      }
    default:
      return state
  }
}

export interface ModalStore<T> extends StoreApi<ModalSnapshot<T>> {
  open(data?: T): void
  close(): void
  setData(patch: Partial<T>): void
  submit(): void
  fail(error: string): void
  succeed(): void
}

export function createModalStore<T>(initialData: T): ModalStore<T> {
  const store = createStore<ModalSnapshot<T>>(() => ({
    isOpen: false,
    status: 'idle',
    data: initialData,
    error: null
  }))

  return {
    ...store,
    open(data?: T): void {
      store.setState((prev) => modalReducer(prev, { type: 'OPEN', data }))
    },
    close(): void {
      store.setState((prev) => modalReducer(prev, { type: 'CLOSE' }))
    },
    setData(patch: Partial<T>): void {
      store.setState((prev) => modalReducer(prev, { type: 'SET_DATA', patch }))
    },
    submit(): void {
      store.setState((prev) => modalReducer(prev, { type: 'SUBMIT' }))
    },
    fail(error: string): void {
      store.setState((prev) => modalReducer(prev, { type: 'FAIL', error }))
    },
    succeed(): void {
      store.setState((prev) => modalReducer(prev, { type: 'SUCCEED' }))
    }
  }
}
export interface DialogSnapshot {
  readonly isOpen: boolean
  readonly status: ModalStatus
  readonly error: string | null
}

export type DialogAction =
  | { type: 'OPEN' }
  | { type: 'CLOSE' }
  | { type: 'SUBMIT' }
  | { type: 'FAIL'; error: string }
  | { type: 'SUCCEED' }

export function dialogReducer(state: DialogSnapshot, action: DialogAction): DialogSnapshot {
  switch (action.type) {
    case 'OPEN':
      return { isOpen: true, status: 'idle', error: null }
    case 'CLOSE':
      return { isOpen: false, status: 'idle', error: null }
    case 'SUBMIT':
      return { ...state, status: 'submitting', error: null }
    case 'FAIL':
      return { ...state, status: 'error', error: action.error }
    case 'SUCCEED':
      return { isOpen: false, status: 'success', error: null }
    default:
      return state
  }
}

export interface DialogStore extends StoreApi<DialogSnapshot> {
  open(): void
  close(): void
  submit(): void
  fail(error: string): void
  succeed(): void
}

export function createDialogStore(): DialogStore {
  const store = createStore<DialogSnapshot>(() => ({
    isOpen: false,
    status: 'idle',
    error: null
  }))

  return {
    ...store,
    open(): void {
      store.setState((prev) => dialogReducer(prev, { type: 'OPEN' }))
    },
    close(): void {
      store.setState((prev) => dialogReducer(prev, { type: 'CLOSE' }))
    },
    submit(): void {
      store.setState((prev) => dialogReducer(prev, { type: 'SUBMIT' }))
    },
    fail(error: string): void {
      store.setState((prev) => dialogReducer(prev, { type: 'FAIL', error }))
    },
    succeed(): void {
      store.setState((prev) => dialogReducer(prev, { type: 'SUCCEED' }))
    }
  }
}


export interface PasswordValidationPayload {
  readonly current: string
  readonly next: string
}

export type PasswordValidationError =
  | { readonly type: 'too_short'; readonly min: number }
  | { readonly type: 'mismatch' }

export function validatePasswordChange(
  current: string,
  next: string,
  confirm: string,
  minLen = 10
): Result<PasswordValidationPayload, PasswordValidationError> {
  if (next.length < minLen) {
    return err({ type: 'too_short', min: minLen })
  }
  if (next !== confirm) {
    return err({ type: 'mismatch' })
  }
  return ok({ current, next })
}
