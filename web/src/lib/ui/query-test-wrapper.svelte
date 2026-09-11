<script lang="ts">
  // Test-only: `createQuery` needs a `QueryClient` in Svelte context, which
  // only a real `QueryClientProvider` instance can set (the context key is a
  // module-private `Symbol`, not something a test can forge). Used as the
  // `wrapper` option in `@testing-library/svelte`'s `render`.
  import type { Snippet } from 'svelte'
  import { QueryClient, QueryClientProvider } from '@tanstack/svelte-query'

  let { children }: { children: Snippet } = $props()

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
</script>

<QueryClientProvider {client}>
  {@render children()}
</QueryClientProvider>
