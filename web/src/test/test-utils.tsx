import '../lib/ui/mdui'
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

function normalizeMediaQuery(query: MediaQueryList): MediaQueryList {
  const addEventListener = typeof query.addEventListener === 'function' ? query.addEventListener.bind(query) : undefined
  const removeEventListener = typeof query.removeEventListener === 'function' ? query.removeEventListener.bind(query) : undefined
  return Object.assign(query, {
    addListener(listener: (event: MediaQueryListEvent) => void): void {
      addEventListener?.('change', listener)
    },
    removeListener(listener: (event: MediaQueryListEvent) => void): void {
      removeEventListener?.('change', listener)
    }
  })
}

if (typeof globalThis.matchMedia !== 'function') {
  globalThis.matchMedia = (media: string) => normalizeMediaQuery({
    matches: false,
    media,
    onchange: null,
    addListener(): void {},
    removeListener(): void {},
    addEventListener(): void {},
    removeEventListener(): void {},
    dispatchEvent(): boolean { return false }
  } as MediaQueryList)
} else {
  const nativeMatchMedia = globalThis.matchMedia.bind(globalThis)
  globalThis.matchMedia = (media: string) => normalizeMediaQuery(nativeMatchMedia(media))
}

import { setLocale } from '../lib/i18n'
setLocale('en')

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderOptions } from '@testing-library/react'
import type { ReactElement, PropsWithChildren } from 'react'
import { MemoryRouter, type MemoryRouterProps } from 'react-router-dom'

export interface RenderAppOptions extends Omit<RenderOptions, 'wrapper'> {
  initialEntries?: MemoryRouterProps['initialEntries']
  initialIndex?: MemoryRouterProps['initialIndex']
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
  {
    initialEntries = ['/'],
    initialIndex,
    queryClient = createTestQueryClient(),
    ...options
  }: RenderAppOptions = {}
) {
  function Wrapper({ children }: PropsWithChildren) {
    return (
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={initialEntries} initialIndex={initialIndex}>
          {children}
        </MemoryRouter>
      </QueryClientProvider>
    )
  }

  return { ...render(element, { wrapper: Wrapper, ...options }), queryClient }
}

export * from '@testing-library/react'
export { renderWithProviders as render }
