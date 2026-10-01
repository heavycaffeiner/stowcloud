import { useMutation } from '@tanstack/react-query'

/** Copies text to the clipboard. `isSuccess` and `isError` describe the last attempt. */
export function useCopyText() {
  return useMutation({ mutationFn: (text: string) => navigator.clipboard.writeText(text) })
}
