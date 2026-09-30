import { useEffect } from 'react'

// Browse pages keep one tab stop in their grid or tree, so focus lands there when the opener is gone.
const FALLBACK = '[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]'

function usable(element: HTMLElement | null): element is HTMLElement {
  return (
    !!element?.isConnected &&
    // Focus on the body means the opener was already gone.
    element !== document.body &&
    !element.hasAttribute('disabled') &&
    !element.hasAttribute('aria-hidden')
  )
}

/**
 * Remembers the focused element when `active` turns true and focuses it again
 * when `active` turns false or the caller unmounts.
 */
export function useRestoreFocus(active: boolean): void {
  useEffect(() => {
    if (!active) return
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return () => {
      queueMicrotask(() => {
        if (usable(opener)) {
          opener.focus()
          if (document.activeElement === opener) return
        }
        document.querySelector<HTMLElement>(FALLBACK)?.focus()
      })
    }
  }, [active])
}
