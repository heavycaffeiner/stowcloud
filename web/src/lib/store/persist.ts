// localStorage for the handful of preferences that outlive a page.
//
// Every read and write is guarded: private mode and a full quota both throw on
// plain access, and a preference is never worth failing a render over.
export function readPref<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  try {
    const saved = localStorage.getItem(key)
    if (allowed.includes(saved as T)) return saved as T
  } catch {
    /* unavailable */
  }
  return fallback
}

export function writePref(key: string, value: string): void {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* unavailable */
  }
}
