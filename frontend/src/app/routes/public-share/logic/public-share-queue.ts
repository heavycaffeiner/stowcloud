import { dropUpload, ShareTooLargeError } from '../../../../lib/api/share'

export type DropItem = { id: number; file: File; status: 'pending' | 'uploading' | 'done' | 'error'; storedAs: string; failure: 'too_large' | 'failed' | null }

type QueueState = {
  queue: DropItem[]
  uploading: boolean
}

export type PublicShareQueue = {
  add: (files: readonly File[], limit: number | null) => void
  retry: (index: number) => void
  removeFailed: (index: number) => void
  readonly items: readonly DropItem[]
  readonly uploading: boolean
}

type QueueOptions = {
  token: string
  setState: (next: Partial<QueueState>) => void
  isActive?: () => boolean
}

export function createPublicShareQueue({ token, setState, isActive = () => true }: QueueOptions) {
  const items: DropItem[] = []
  let uploading = false
  let nextId = 0

  const publish = (): void => { if (isActive()) setState({ queue: [...items], uploading }) }
  const run = async (): Promise<void> => {
    if (uploading) return
    uploading = true
    publish()
    try {
      for (const item of items) {
        if (item.status !== 'pending') continue
        item.status = 'uploading'
        publish()
        try {
          item.storedAs = await dropUpload(token, item.file)
          item.status = 'done'
        } catch (error) {
          item.status = 'error'
          item.failure = error instanceof ShareTooLargeError ? 'too_large' : 'failed'
        }
        publish()
      }
    } finally {
      uploading = false
      publish()
      if (items.some((item) => item.status === 'pending')) queueMicrotask(() => void run())
    }
  }
  const add = (files: readonly File[], limit: number | null): void => {
    for (const file of files) {
      const tooLarge = limit !== null && file.size > limit
      items.push({ id: nextId++, file, status: tooLarge ? 'error' : 'pending', storedAs: '', failure: tooLarge ? 'too_large' : null })
    }
    publish()
    queueMicrotask(() => void run())
  }
  const retry = (index: number): void => {
    const item = items[index]
    if (!item || item.status !== 'error') return
    item.status = 'pending'
    item.storedAs = ''
    item.failure = null
    publish()
    queueMicrotask(() => void run())
  }
  const removeFailed = (index: number): void => {
    if (items[index]?.status !== 'error') return
    items.splice(index, 1)
    publish()
  }
  return { add, retry, removeFailed, get items(): readonly DropItem[] { return items }, get uploading(): boolean { return uploading } } satisfies PublicShareQueue
}
