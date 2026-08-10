// tools/design-grid/jsdom-shims.ts — imported first by component.test.ts.
//
// jsdom ships no window.matchMedia, and svelte/motion evaluates a
// prefers-reduced-motion MediaQuery at module scope. m3-svelte's Slider pulls
// that in, so the whole suite fails to import before a single component
// mounts. A stub answering "no match" is the right default here: the component
// layer reads markup and inline styles, never motion.
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false
    }) as MediaQueryList
}

export {}
