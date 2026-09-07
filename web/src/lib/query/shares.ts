// Public share links for one path.
import { mutationOptions, queryOptions } from '@tanstack/svelte-query'
import { api, type ShareLinkCreateReq, type ShareLinkPatchReq } from '../api/client'
import { queryClient } from './client'
import { keys } from './keys'

export function shareLinksQuery(path: string | undefined, enabled = true) {
  return queryOptions({ queryKey: keys.shareLinks(path), queryFn: () => api.sharesList(path), enabled })
}

// Both listings, because a link appears in two: the owner's own and the
// administrative overview. Revoking from one screen left the other showing a
// link that no longer exists.
function invalidateShareLinks(): void {
  void queryClient.invalidateQueries({ queryKey: ['share-links'] })
  void queryClient.invalidateQueries({ queryKey: keys.adminLinks() })
}

export function shareCreateMutation() {
  return mutationOptions({ mutationFn: (req: ShareLinkCreateReq) => api.shareCreate(req), onSuccess: invalidateShareLinks })
}

export function shareUpdateMutation() {
  return mutationOptions({
    mutationFn: ({ id, patch }: { id: number; patch: ShareLinkPatchReq }) => api.shareUpdate(id, patch),
    onSuccess: invalidateShareLinks
  })
}

export function shareDeleteMutation() {
  return mutationOptions({ mutationFn: (id: number) => api.shareDelete(id), onSuccess: invalidateShareLinks })
}
