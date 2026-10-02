import { useEventListener } from './use-event-listener'

/** Asks the browser to confirm leaving the page while `active`. */
export function useBeforeUnload(active: boolean): void {
  useEventListener(active ? window : null, 'beforeunload', (event) => event.preventDefault())
}
