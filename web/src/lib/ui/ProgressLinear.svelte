<script lang="ts">
  // Adapter over m3-svelte's LinearProgress.
  //
  // The framework ships no indeterminate *linear* bar (`LoadingIndicator` is
  // its only indeterminate affordance) so a null value renders that instead.
  // Only JobTray reaches it, for the moment between a job being enqueued and
  // its total being counted; a 0%-wide bar there would claim progress we do
  // not have.
  //
  // `tone` remaps `--m3c-primary` for the subtree rather than restating a fill
  // colour: LinearProgress paints its filled portion from that token, so the
  // override reaches it without overriding the framework's own styling.
  import { LinearProgress, LoadingIndicator } from 'm3-svelte'
  import { t } from '../i18n'

  interface Props {
    /** 0..1, or omit for an indeterminate indicator. */
    value?: number | null
    label?: string
    /** The password-strength meter reuses the error/tertiary/primary roles as
     *  a 3-step scale; MD3 has no dedicated warning/success role. */
    tone?: 'primary' | 'weak' | 'fair' | 'strong'
  }

  // See ProgressCircular for why `label` has no default value.
  let { value = null, label, tone = 'primary' }: Props = $props()
  const resolved = $derived(label ?? t('progress.progress'))

  const TONE_ROLE: Record<string, string | undefined> = {
    weak: '--m3c-primary: var(--m3c-error);',
    fair: '--m3c-primary: var(--m3c-tertiary);'
  }
</script>

{#if value === null}
  <LoadingIndicator size={16} aria-label={resolved} />
{:else}
  <div class="bar" style={TONE_ROLE[tone]}>
    <LinearProgress percent={Math.round(value * 100)} aria-label={resolved} />
  </div>
{/if}

<style>
  /* Layout only: LinearProgress sits at its natural width, and every call
     site wants it to fill its row. */
  .bar {
    width: 100%;
  }
</style>
