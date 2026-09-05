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
</script>

{#if value === null}
  <LoadingIndicator {size} aria-label={resolved} />
{:else}
  <CircularProgress percent={Math.round(value * 100)} {size} aria-label={resolved} />
{/if}
