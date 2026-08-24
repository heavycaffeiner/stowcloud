<script lang="ts">
  // Folder permission management — the only screen that actually operates the
  // product's first requirement (deny by default: users see no folders
  // except the ones an admin explicitly granted). Reachable
  // by picking a user in `UserManagementSection` or a group in
  // `GroupManagementSection` and opening this component — the `principal`
  // prop pins which one, so there's no principal-selection UI inside this
  // screen itself.
  //
  // GET /api/admin/shares, GET/POST /api/admin/grants,
  // PATCH/DELETE /api/admin/grants/{id}.
  //
  // Virtual root: what this creates is an independent
  // rule, not a checkbox in a folder tree. Grant permissions on `Photos/a`
  // and `Photos/b` separately and the user sees two top-level items, `a` and
  // `b` — they never learn `Photos` exists; the hint text beside the subpath
  // input below says so. Showing a folder tree with checkboxes would give
  // the opposite impression: that a real tree exists underneath.
  //
  // Deny beats allow (DENY wins at equal depth):
  // checking the same permission in both allow and deny within one rule
  // still saves as-is — the server doesn't normalize it (see the
  // `admin_grants` test `a_bit_present_in_both_allow_and_deny_round_trips_unmodified`
  // in `go/internal/httpapi/handler`) — but deny wins at evaluation time.
  // The table below shows that state plainly, with a warning, rather than
  // hiding it.
  import { t } from '../../i18n'
  import { api, ApiError, type AdminGrant, type AdminShare, type GrantPermName, type GrantPrincipal } from '../../api/client'
  import { ALL_GRANT_PERMS } from '../../api/client'
  import Button from '../Button.svelte'
  import Checkbox from '../Checkbox.svelte'
  import Chip from '../Chip.svelte'
  import Dialog from '../Dialog.svelte'
  import { Icon, SelectOutlined } from 'm3-svelte'
  import { icons } from '../../icons'
  import IconButton from '../IconButton.svelte'
  import ListItem from '../ListItem.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import TextField from '../TextField.svelte'

  interface Props {
    /** Who these grants belong to — a user id or a group id, never both. */
    principal: GrantPrincipal
    /** Display name for the hint/dialog copy below — the caller already has
     *  it (`AdminUser.display_name`/`AdminGroup.name`), so this screen
     *  doesn't need its own user-vs-group lookup just to render a label. */
    label: string
  }
  const { principal, label }: Props = $props()

  const PERM_LABEL: Record<GrantPermName, string> = {
    read: t('common.read'),
    write: t('grant.write'),
    create: t('grant.create'),
    delete: t('common.delete'),
    rename: t('grant.rename'),
    move: t('common.move'),
    share: t('common.share_links'),
    download: t('common.download')
  }

  let shares = $state<AdminShare[]>([])
  let grants = $state<AdminGrant[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      const [s, g] = await Promise.all([
        api.adminListShares(),
        api.adminListGrants(principal.kind === 'user' ? { userId: principal.id } : { groupId: principal.id })
      ])
      shares = s
      grants = g
    } catch {
      loadError = t('grant.could_not_load_permission_list')
    } finally {
      loading = false
    }
  }

  load()

  function shareName(id: number): string {
    return shares.find((s) => s.id === id)?.name ?? t('grant.share', { id })
  }

  // Permission detail (8 chips × allow/deny) is collapsed by default. At just
  // 12 grants that's 96 chips, doubling the screen's scroll length, and a
  // one-line summary already answers most of the question ("what can this
  // person do in this folder?") — expand only when the exact bit combination
  // matters.
  let expandedIds = $state<Set<number>>(new Set())

  function toggleExpanded(id: number): void {
    const next = new Set(expandedIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    expandedIds = next
  }

  /** One-line summary — e.g. "Read · Download", or "Full permissions" when
   *  all 8 bits are on. */
  function allowSummary(g: AdminGrant): string {
    if (g.allow.length === 0) return t('grant.no_permissions_granted')
    if (g.allow.length === ALL_GRANT_PERMS.length) return t('grant.full_permissions')
    return g.allow.map((p) => PERM_LABEL[p]).join('·')
  }

  // ── add folder ──

  let addOpen = $state(false)
  // m3-svelte's Select is a real `<select>`, so its value is a string; the
  // API takes the numeric share id. '' means "nothing picked yet".
  let addShareId = $state('')
  const shareOptions = $derived(shares.map((s) => ({ value: String(s.id), text: s.name })))
  let addSubpath = $state('')
  let addAllow = $state<Set<GrantPermName>>(new Set(['read', 'download']))
  let addDeny = $state<Set<GrantPermName>>(new Set())
  let addInherit = $state(true)
  let addLabel = $state('')
  let addError = $state<string | null>(null)
  let adding = $state(false)

  function openAdd(): void {
    addShareId = shares[0] ? String(shares[0].id) : ''
    addSubpath = ''
    addAllow = new Set(['read', 'download'])
    addDeny = new Set()
    addInherit = true
    addLabel = ''
    addError = null
    addOpen = true
  }

  function closeAdd(): void {
    if (adding) return
    addOpen = false
  }

  async function submitAdd(): Promise<void> {
    addError = null
    if (addShareId === '') {
      addError = t('grant.select_share')
      return
    }
    if (addAllow.size === 0 && addDeny.size === 0) {
      addError = t('grant.select_at_least_one_permission')
      return
    }
    adding = true
    try {
      const created = await api.adminCreateGrant({
        principal,
        share: Number(addShareId),
        subpath: addSubpath.trim(),
        allow: [...addAllow],
        deny: [...addDeny],
        inherit: addInherit,
        label: addLabel.trim() || undefined
      })
      grants = [...grants, created]
      addOpen = false
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.invalid_name') {
        addError = t('grant.select_at_least_one_permission')
      } else if (err instanceof ApiError && err.code === 'fs.not_found') {
        addError = t('common.share_no_longer_exists')
      } else {
        addError = t('common.could_not_add_folder')
      }
    } finally {
      adding = false
    }
  }

  // ── edit folder ──

  let editTarget = $state<AdminGrant | null>(null)
  let editAllow = $state<Set<GrantPermName>>(new Set())
  let editDeny = $state<Set<GrantPermName>>(new Set())
  let editInherit = $state(true)
  let editLabel = $state('')
  let editError = $state<string | null>(null)
  let editing = $state(false)

  function openEdit(g: AdminGrant): void {
    editTarget = g
    editAllow = new Set(g.allow)
    editDeny = new Set(g.deny)
    editInherit = g.inherit
    editLabel = g.label ?? ''
    editError = null
  }

  function closeEdit(): void {
    if (editing) return
    editTarget = null
  }

  async function submitEdit(): Promise<void> {
    if (!editTarget) return
    editError = null
    if (editAllow.size === 0 && editDeny.size === 0) {
      editError = t('grant.select_at_least_one_permission')
      return
    }
    editing = true
    try {
      const updated = await api.adminUpdateGrant(editTarget.id, {
        allow: [...editAllow],
        deny: [...editDeny],
        inherit: editInherit,
        label: editLabel.trim() || null
      })
      grants = grants.map((g) => (g.id === updated.id ? updated : g))
      editTarget = null
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.invalid_name') {
        editError = t('grant.select_at_least_one_permission')
      } else {
        editError = t('common.could_not_save_change')
      }
    } finally {
      editing = false
    }
  }

  // ── remove folder ──

  let deleteTarget = $state<AdminGrant | null>(null)
  let deleting = $state(false)
  let deleteError = $state<string | null>(null)

  function askDelete(g: AdminGrant): void {
    deleteError = null
    deleteTarget = g
  }

  function closeDelete(): void {
    if (deleting) return
    deleteTarget = null
  }

  async function confirmDelete(): Promise<void> {
    if (!deleteTarget) return
    deleting = true
    deleteError = null
    try {
      await api.adminDeleteGrant(deleteTarget.id)
      grants = grants.filter((g) => g.id !== deleteTarget!.id)
      deleteTarget = null
    } catch {
      deleteError = t('common.could_not_remove')
    } finally {
      deleting = false
    }
  }

  function togglePerm(set: Set<GrantPermName>, name: GrantPermName): Set<GrantPermName> {
    const next = new Set(set)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    return next
  }
</script>

<section class="sc-grants">
  <p class="sc-grants__hint">
    <strong>{label}</strong>{t('grant.sees_only_folders_granted_here')}
  </p>

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-grants__error">{loadError}</p>
  {:else}
    {#if grants.length === 0}
      <div class="sc-grants__empty">
        <Icon icon={icons['folder-tree']} size={28} />
        <p>{t('grant.no_folders_granted_yet_signing')}</p>
      </div>
    {:else}
      <ul class="sc-grants__list">
        {#each grants as g (g.id)}
          {@const overlap = g.allow.filter((p) => g.deny.includes(p))}
          {@const expanded = expandedIds.has(g.id)}
          <li>
            <ListItem>
              {#snippet headline()}
                <span class="sc-grants__name">{g.label || shareName(g.share)}</span>
                {#if !g.inherit}<Chip variant="assist">{t('grant.path_only')}</Chip>{/if}
              {/snippet}
              {#snippet supporting()}
                <span class="sc-grants__path">{shareName(g.share)}{g.subpath ? ` / ${g.subpath}` : t('grant.root')}</span>
                <span class="sc-grants__summary">
                  {allowSummary(g)}
                  {#if g.deny.length > 0}<span class="sc-grants__summary-deny">
                      · {t('grant.denied', { perms: g.deny.map((p) => PERM_LABEL[p]).join(', ') })}</span>{/if}
                </span>
                {#if overlap.length > 0}
                  <span class="sc-grants__warning">
                    <Icon icon={icons.warning} size={14} />
                    {t('grant.appears_both_allow_deny_so', {
                      perms: overlap.map((p) => PERM_LABEL[p]).join(', ')
                    })}
                  </span>
                {/if}
                {#if expanded}
                  <span class="sc-grants__perms">
                    {#each g.allow as p (p)}<Chip variant="filter" selected>{PERM_LABEL[p]}</Chip>{/each}
                    {#each g.deny as p (p)}<Chip variant="input">{t('grant.denied', { perms: PERM_LABEL[p] })}</Chip>{/each}
                  </span>
                {/if}
              {/snippet}
              {#snippet trailing()}
                <IconButton label={expanded ? t('grant.collapse_permission_details') : t('grant.expand_permission_details')} onclick={() => toggleExpanded(g.id)}>
                  <span class="sc-grants__chevron" class:sc-grants__chevron--open={expanded}>
                    <Icon icon={icons['chevron-right']} size={18} />
                  </span>
                </IconButton>
                <IconButton label={t('common.edit', { name: g.label || shareName(g.share) })} onclick={() => openEdit(g)}>
                  <Icon icon={icons.settings} size={18} />
                </IconButton>
                <IconButton label={t('common.remove', { name: g.label || shareName(g.share) })} onclick={() => askDelete(g)}>
                  <Icon icon={icons.delete} size={18} />
                </IconButton>
              {/snippet}
            </ListItem>
          </li>
        {/each}
      </ul>
    {/if}

    <Button variant="tonal" onclick={openAdd}>
      {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
      {t('common.add_folder')}
    </Button>
  {/if}
</section>

<Dialog open={addOpen} title={t('common.add_folder')} onclose={closeAdd}>
  <form class="sc-grants__form" onsubmit={(e) => (e.preventDefault(), submitAdd())}>
    <SelectOutlined label={t('common.share')} width="100%" options={shareOptions} bind:value={addShareId} />

    <TextField label={t('grant.subpath_leave_empty_whole_share')} bind:value={addSubpath} placeholder={t('grant.e_g_vacation')} autocomplete="off" />
    <p class="sc-grants__field-hint">
      {t('grant.left_empty_whole_share_appears')}
    </p>

    <div class="sc-grants__permgrid">
      <div class="sc-grants__permgrid-header">
        <span></span>
        <span>{t('grant.allow')}</span>
        <span>{t('grant.deny')}</span>
      </div>
      {#each ALL_GRANT_PERMS as p (p)}
        <div class="sc-grants__permgrid-row">
          <span>{PERM_LABEL[p]}</span>
          <Checkbox checked={addAllow.has(p)} label={t('grant.allow_2', { perm: PERM_LABEL[p] })} onchange={() => (addAllow = togglePerm(addAllow, p))} />
          <Checkbox checked={addDeny.has(p)} label={t('grant.deny_2', { perm: PERM_LABEL[p] })} onchange={() => (addDeny = togglePerm(addDeny, p))} />
        </div>
      {/each}
    </div>

    <Checkbox checked={addInherit} label={t('grant.apply_subfolders')} onchange={(v) => (addInherit = v)} />
    <TextField label={t('grant.display_name_optional')} bind:value={addLabel} placeholder={t('grant.defaults_folder_name')} autocomplete="off" />

    {#if addError}<p class="sc-grants__error" role="alert">{addError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeAdd} disabled={adding}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submitAdd} loading={adding}>{t('common.add')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!editTarget} title={t('grant.edit_folder_permission')} onclose={closeEdit}>
  {#if editTarget}
    <form class="sc-grants__form" onsubmit={(e) => (e.preventDefault(), submitEdit())}>
      <p class="sc-grants__field-hint">
        {shareName(editTarget.share)}{editTarget.subpath ? ` / ${editTarget.subpath}` : t('grant.root')}
        {t('grant.share_path_cannot_changed_grant')}
      </p>

      <div class="sc-grants__permgrid">
        <div class="sc-grants__permgrid-header">
          <span></span>
          <span>{t('grant.allow')}</span>
          <span>{t('grant.deny')}</span>
        </div>
        {#each ALL_GRANT_PERMS as p (p)}
          <div class="sc-grants__permgrid-row">
            <span>{PERM_LABEL[p]}</span>
            <Checkbox checked={editAllow.has(p)} label={t('grant.allow_2', { perm: PERM_LABEL[p] })} onchange={() => (editAllow = togglePerm(editAllow, p))} />
            <Checkbox checked={editDeny.has(p)} label={t('grant.deny_2', { perm: PERM_LABEL[p] })} onchange={() => (editDeny = togglePerm(editDeny, p))} />
          </div>
        {/each}
      </div>
      {#if [...editAllow].some((p) => editDeny.has(p))}
        <p class="sc-grants__warning">
          <Icon icon={icons.warning} size={14} />
          {t('grant.permission_listed_both_allow_deny')}
        </p>
      {/if}

      <Checkbox checked={editInherit} label={t('grant.apply_subfolders')} onchange={(v) => (editInherit = v)} />
      <TextField label={t('grant.display_name_optional')} bind:value={editLabel} placeholder={t('grant.defaults_folder_name')} autocomplete="off" />

      {#if editError}<p class="sc-grants__error" role="alert">{editError}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeEdit} disabled={editing}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submitEdit} loading={editing}>{t('common.save')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!deleteTarget} title={t('grant.remove_folder_permission')} onclose={closeDelete}>
  <p>
    {t('grant.access_removed_immediately', {
      name: deleteTarget?.label || (deleteTarget ? shareName(deleteTarget.share) : '')
    })}
    {t('grant.will_not_see_folder_from', { principal: label })}
  </p>
  {#if deleteError}<p class="sc-grants__error" role="alert">{deleteError}</p>{/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeDelete} disabled={deleting}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmDelete} loading={deleting}>{t('common.remove_2')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-grants {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .sc-grants__hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-grants__empty {
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
  .sc-grants__empty p {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-grants__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-grants__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-grants__list :global(.sc-list-item) {
    align-items: flex-start;
    padding-block: 8px;
  }
  .sc-grants__list :global(.sc-list-item__supporting) {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-top: 4px;
  }
  .sc-grants__name {
    font-weight: 500;
    color: var(--m3c-on-surface);
  }
  .sc-grants__path {
    color: var(--m3c-on-surface-variant);
  }
  .sc-grants__summary {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-grants__summary-deny {
    color: var(--m3c-error);
  }
  .sc-grants__perms {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 4px;
  }
  .sc-grants__chevron {
    display: inline-flex;
    transition: transform var(--m3-easing-fast);
  }
  .sc-grants__chevron--open {
    transform: rotate(90deg);
  }
  .sc-grants__warning {
    display: flex;
    align-items: center;
    gap: 4px;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-grants__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-grants__form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: min(360px, 80vw);
  }
  .sc-grants__field-hint {
    margin: calc(-1 * 8px) 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-grants__permgrid {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-grants__permgrid-header,
  .sc-grants__permgrid-row {
    display: grid;
    grid-template-columns: 1fr 64px 64px;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
  }
  .sc-grants__permgrid-header {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    background: var(--m3c-surface-container-highest);
  }
  .sc-grants__permgrid-row + .sc-grants__permgrid-row {
    border-top: 1px solid var(--m3c-outline-variant);
  }
</style>
