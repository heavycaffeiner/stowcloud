<script lang="ts">
  // Active sessions — DESIGN-AUTH.md §3.2/§10: IP/UA are display-only
  // records, not an authentication condition. Individual sessions can be
  // revoked, and the current session gets a badge.
  import { onMount } from 'svelte'
  import { api, ApiError, type ActiveSession } from '../../api/client'
  import { formatDateNs, t } from '../../i18n'
  import { describeUserAgent } from '../../format/user-agent'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import ListItem from '../ListItem.svelte'
  import IconButton from '../IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import ProgressCircular from '../ProgressCircular.svelte'

  let sessions = $state<ActiveSession[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)
  let revokeTarget = $state<ActiveSession | null>(null)
  let revoking = $state(false)

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      sessions = await api.listSessions()
    } catch {
      loadError = t('common.could_not_load_list')
    } finally {
      loading = false
    }
  }

  onMount(load)

  function askRevoke(s: ActiveSession): void {
    revokeTarget = s
  }

  function closeRevoke(): void {
    revokeTarget = null
  }

  async function confirmRevoke(): Promise<void> {
    if (!revokeTarget) return
    revoking = true
    try {
      await api.revokeSession(revokeTarget.id_hash)
      revokeTarget = null
      await load()
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        revokeTarget = null
        await load()
      }
    } finally {
      revoking = false
    }
  }
</script>

<div class="sc-sessions">
  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-sessions__error">{loadError}</p>
  {:else}
    <ul class="sc-sessions__list">
      {#each sessions as s (s.id_hash)}
        <li>
          <ListItem>
            {#snippet headline()}
              {s.ip_first ?? t('session.unknown_location')}
              {#if s.current}<span class="sc-sessions__badge">{t('session.current_session')}</span>{/if}
            {/snippet}
            {#snippet supporting()}
              <span title={s.ua_first ?? undefined}>{describeUserAgent(s.ua_first)}</span>
              · {t('session.last_active', { date: formatDateNs(s.last_seen_ns) })}
            {/snippet}
            {#snippet trailing()}
              {#if !s.current}
                <IconButton label={t('session.sign_out_session')} onclick={() => askRevoke(s)}><Icon icon={icons.delete} size={18} /></IconButton>
              {/if}
            {/snippet}
          </ListItem>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<Dialog open={!!revokeTarget} title={t('session.sign_out_session_2')} onclose={closeRevoke}>
  <p>{t('session.device_signed_out_immediately', { where: revokeTarget?.ip_first ?? t('session.unknown_location') })}</p>
  {#snippet actions()}
    <Button variant="text" onclick={closeRevoke}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmRevoke} loading={revoking}>{t('common.sign_out')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-sessions__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-sessions__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-sessions__error {
    color: var(--m3c-on-surface-variant);
  }
  .sc-sessions__badge {
    display: inline-flex;
    align-items: center;
    height: 24px;
    padding-inline: 8px;
    margin-inline-start: 8px;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-primary-container);
    color: var(--m3c-on-primary-container);
    @apply --m3-label-small;
    vertical-align: middle;
  }
</style>
