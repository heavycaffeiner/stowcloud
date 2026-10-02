// The folder the browser last showed, so navigation back to Files and a search
// started from elsewhere both land where the user left off.
import { signal } from '@preact/signals-react'

export const lastFolder = signal('/')
