<script lang="ts">
  // True root layout: shared by EVERY route, including /s/[token]. Kept
  // minimal on purpose: global styles, theme init, and the query client.
  // Anything heavier (nav rail, upload tray) lives in (app)/+layout.svelte so
  // the public share bundle never pays for it.
  import '../app.css'
  // m3-svelte's ripple: one set of document listeners driving every element
  // tagged `.m3-layer`, imported for side effect. It belongs here rather than
  // in (app)/ because the share page uses framework buttons too.
  import 'm3-svelte/etc/layer'
  import type { Snippet } from 'svelte'
  import { QueryClientProvider } from '@tanstack/svelte-query'
  import { localeTag } from '../lib/i18n'
  import { queryClient } from '../lib/query/client'
  import { ui } from '../lib/store/ui.store'

  interface Props {
    children: Snippet
  }
  let { children }: Props = $props()

  $effect(() => {
    const root = document.documentElement
    if (ui.state.theme === 'system') root.removeAttribute('data-theme')
    else root.setAttribute('data-theme', ui.state.theme)
  })

  // `app.html` ships `lang="ko"` for the first paint; once the saved locale is
  // known the document has to agree with what is on screen, or a screen reader
  // reads English copy with Korean pronunciation rules and `:lang()` line-break
  // rules apply to the wrong language.
  $effect(() => {
    document.documentElement.lang = localeTag()
  })
</script>

<QueryClientProvider client={queryClient}>
  {@render children()}
</QueryClientProvider>
