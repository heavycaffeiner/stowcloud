if (!('ResizeObserver' in globalThis)) {
  class TestResizeObserver {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  Object.assign(globalThis, { ResizeObserver: TestResizeObserver })
}
HTMLElement.prototype.getAnimations = () => [] as Animation[]
HTMLElement.prototype.animate = () => {
  const listeners = new Set<() => void>()
  const animation = {
    cancel(): void {
      for (const listener of listeners) listener()
    },
    finished: Promise.resolve(),
    addEventListener(type: string, listener: EventListener): void {
      if (type === 'cancel' || type === 'finish') listeners.add(() => listener(new Event(type)))
    },
    removeEventListener(): void {}
  }
  queueMicrotask(() => {
    for (const listener of listeners) listener()
  })
  return animation as unknown as Animation
}
if (typeof Range.prototype.getClientRects !== 'function') {
  Range.prototype.getClientRects = () => [] as unknown as DOMRectList
}
if (typeof Range.prototype.getBoundingClientRect !== 'function') {
  Range.prototype.getBoundingClientRect = () => new DOMRect()
}

// jsdom has no matchMedia, and the one mdui's ssr-window fills in returns an empty object.
globalThis.matchMedia = (media: string) =>
  ({
    matches: false,
    media,
    onchange: null,
    addListener(): void {},
    removeListener(): void {},
    addEventListener(): void {},
    removeEventListener(): void {},
    dispatchEvent(): boolean {
      return false
    }
  }) as MediaQueryList

import { setLocale } from '../i18n'
import { I18nextProvider } from 'react-i18next'
import { i18n } from '../i18n/state'
await setLocale('en')

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderOptions } from '@testing-library/react'
import type { ReactElement, PropsWithChildren } from 'react'

export interface RenderAppOptions extends Omit<RenderOptions, 'wrapper'> {
  queryClient?: QueryClient
}

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: Infinity },
      mutations: { retry: false }
    }
  })
}

export function renderWithProviders(
  element: ReactElement,
  { queryClient = createTestQueryClient(), ...options }: RenderAppOptions = {}
) {
  function Wrapper({ children }: PropsWithChildren) {
    return (
      <I18nextProvider i18n={i18n}>
        <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
      </I18nextProvider>
    )
  }

  return { ...render(element, { wrapper: Wrapper, ...options }), queryClient }
}

export * from '@testing-library/react'
export { renderWithProviders as render }
