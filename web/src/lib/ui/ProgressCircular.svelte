<script lang="ts">
  // Adapter over m3-svelte's two circular progress components. The framework
  // splits what this used to be one component for: `CircularProgress` is
  // determinate and *requires* a percentage, `LoadingIndicator` is the
  // indeterminate one. `value === null` (the default, and what every current
  // call site uses) picks the latter.
  import { CircularProgress, LoadingIndicator } from 'm3-svelte'
  import { t } from '../i18n'

  interface Props {
    /** 0..1, or null for indeterminate. */
    value?: number | null
    size?: number
    label?: string
  }

  // No default *value* for `label`: one evaluated at construction would keep
  // whichever locale was live then. Resolved reactively instead.
  let { value = null, size = 24, label }: Props = $props()
  const resolved = $derived(label ?? t('progress.loading'))
  const fraction = $derived(
    typeof value === 'number' && Number.isFinite(value) ? Math.min(Math.max(value, 0), 1) : 0
  )
  const indeterminate = $derived(
    value === null || typeof value !== 'number' || !Number.isFinite(value)
  )
  const percent = $derived(Math.round(fraction * 100))
</script>

{#if indeterminate}
  <LoadingIndicator {size} aria-label={resolved} />
{:else}
  <CircularProgress percent={percent} {size} aria-label={resolved} />
{/if}
