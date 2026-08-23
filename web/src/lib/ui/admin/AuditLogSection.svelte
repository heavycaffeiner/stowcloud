<script lang="ts">
  // Audit log browsing — read-only over
  // `GET /api/admin/audit`, cursor-paginated on `rowid` (newest first).
  // `go/internal/auth/audit.go` is the source;
  // this screen adds no write path, only filters (actor, event name) and a
  // "Load more" cursor to keep scanning older rows.
  import { api, type AdminUser, type AuditRow } from '../../api/client'
  import Button from '../Button.svelte'
  import { Icon, SelectOutlined } from 'm3-svelte'
  import { icons } from '../../icons'
  import ListItem from '../ListItem.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import TextField from '../TextField.svelte'
  import { formatDateNs, t } from '../../i18n'

  let rows = $state<AuditRow[]>([])
  let users = $state<AdminUser[]>([])
  let next = $state<number | null>(null)
  let loading = $state(true)
  let loadingMore = $state(false)
  let loadError = $state<string | null>(null)

  let eventFilter = $state('')
  // m3-svelte's Select is string-valued (it is a real `<select>`); the API
  // wants `number | undefined`, so the empty string is the "All" sentinel.
  let actorFilter = $state('')
  const actorId = $derived(actorFilter === '' ? undefined : Number(actorFilter))
  const actorOptions = $derived([
    { value: '', text: t('audit.all') },
    ...users.map((u) => ({ value: String(u.id), text: u.display_name || u.name }))
  ])

  function actorName(id: number | null): string {
    if (id === null) return t('common.system')
    return users.find((u) => u.id === id)?.display_name || t('common.user', { id })
  }

  async function load(reset: boolean): Promise<void> {
    if (reset) {
      loading = true
      loadError = null
    } else {
      loadingMore = true
    }
    try {
      const page = await api.adminListAudit({
        actor: actorId,
        event: eventFilter.trim() || undefined,
        before: reset ? undefined : (next ?? undefined)
      })
      rows = reset ? page.rows : [...rows, ...page.rows]
      next = page.next
    } catch {
      loadError = t('audit.could_not_load_audit_log')
    } finally {
      loading = false
      loadingMore = false
    }
  }

  async function init(): Promise<void> {
    try {
      users = await api.adminListUsers()
    } catch {
      // Names fall back to `User #id` below — not fatal to the log itself.
    }
    await load(true)
  }

  init()

  function applyFilters(e?: SubmitEvent): void {
    e?.preventDefault()
    load(true)
  }
</script>

<section class="sc-audit-log">
  <h2>{t('common.audit_log')}</h2>
  <form class="sc-audit-log__filters" onsubmit={applyFilters}>
    <TextField label={t('audit.event_name')} placeholder={t('audit.e_g_auth_login')} bind:value={eventFilter} autocomplete="off" />
    <SelectOutlined label={t('common.user_2')} width="100%" options={actorOptions} bind:value={actorFilter} />
    <Button variant="tonal" onclick={() => applyFilters()}>{t('audit.apply_filters')}</Button>
  </form>

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-audit-log__error">{loadError}</p>
  {:else if rows.length === 0}
    <div class="sc-audit-log__empty">
      <Icon icon={icons.list} size={28} />
      <p>{t('audit.no_records_match_these_filters')}</p>
    </div>
  {:else}
    <ul class="sc-audit-log__list">
      {#each rows as r (r.rowid)}
        <li>
          <ListItem>
            {#snippet leading()}
              <Icon icon={icons[r.ok ? 'check' : 'warning']} size={20} />
            {/snippet}
            {#snippet headline()}
              <span class="sc-audit-log__event">{r.event}</span>
              <span class="sc-audit-log__result" class:sc-audit-log__result--fail={!r.ok}>
                {r.ok ? t('audit.success') : t('audit.failure')}
              </span>
            {/snippet}
            {#snippet supporting()}
              {formatDateNs(r.ts_ns)} · {actorName(r.actor)}
              {#if r.target}· {r.target}{/if}
              {#if r.ip}· {r.ip}{/if}
              {#if r.detail}· {r.detail}{/if}
            {/snippet}
          </ListItem>
        </li>
      {/each}
    </ul>
    {#if next !== null}
      <div class="sc-audit-log__more">
        <Button variant="text" onclick={() => load(false)} loading={loadingMore}>{t('audit.load_more')}</Button>
      </div>
    {/if}
  {/if}
</section>

<style>
  .sc-audit-log {
    container-name: sc-audit-log;
    container-type: inline-size;
  }
  .sc-audit-log h2 {
    /* 16px, the step every other screen puts between a section title and its
       first content. 8px read as the filters belonging to the title rather
       than following it. */
    margin: 0 0 16px;
    @apply --m3-title-large;
  }
  .sc-audit-log__filters {
    display: flex;
    align-items: flex-end;
    gap: 16px;
    margin-bottom: 16px;
  }
  /* `TextField.svelte` sizes the framework's box off its own wrapper
     (`width: 100%`), which is right in the column layouts most callers use and
     circular in a row: the wrapper's automatic width comes from content
     measured as a percentage of the wrapper, so it resolved to zero, and the
     field's label — 3 wrapped lines of "Event name" — landed on top of the
     "User" select. Same defect, same fix as `.sc-browse__search`: the row says
     how much of itself each control takes. `max-width` because a filter pair
     stretched across a 1440px admin pane is a form nobody asked for. */
  @container sc-audit-log (min-width: 600px) {
    .sc-audit-log__filters > :global(.field),
    .sc-audit-log__filters > :global(div.m3-container) {
      flex: 1 1 0;
      max-width: 280px;
    }
  }
  @container sc-audit-log (max-width: 599.98px) {
    .sc-audit-log__filters {
      flex-direction: column;
      align-items: stretch;
    }
  }
  .sc-audit-log__error {
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-audit-log__empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 32px 16px;
    color: var(--m3c-on-surface-variant);
    text-align: center;
    border: 1px dashed var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
  }
  .sc-audit-log__empty p {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-audit-log__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-audit-log__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-audit-log__event {
    font-weight: 500;
  }
  .sc-audit-log__result {
    margin-inline-start: 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-audit-log__result--fail {
    color: var(--m3c-error);
  }
  .sc-audit-log__more {
    display: flex;
    justify-content: center;
    margin-top: 16px;
  }
</style>
