<script lang="ts">
  // Folder share management: adding, renaming and removing shared folders.
  // GET/POST /api/admin/shares, PATCH/DELETE /api/admin/shares/{id}.
  //
  // A share is served from one of three places, chosen when it is added and
  // fixed afterwards: a folder on this server, which is what a bind mount or
  // a container volume arrives as; an S3-compatible bucket; or a VeraCrypt
  // container file. This screen is where every share comes from: no file
  // declares any, so a deployment serves nothing until somebody adds one
  // here.
  //
  // This is a distinct, adjacent screen from `GrantManagementSection`: that
  // one decides *who* can see a share (or a subpath of it); this one decides
  // *which folders exist* as shares in the first place. A share still needs
  // a grant before anyone but an admin can see it.
  import { createMutation, createQuery } from '@tanstack/svelte-query'
  import { t } from '../../i18n'
  import {
    api,
    ApiError,
    type AdminShare,
    type CreateShareReq,
    type SMBOutcome,
    type ShareBackend,
    type ShareS3Config,
    type ShareVeracryptConfig,
    type UpdateShareReq
  } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import { adminShareMutation, adminSharesQuery } from '../../query/admin'
  import { queryClient } from '../../query/client'
  import { generateSalt, deriveKeys, makeVerifier, type DerivedKeys } from '../../crypto/e2ee'
  import { invalidateEncryptedShares } from '../../crypto/encrypted-shares'
  import { clean } from '@noble/ciphers/utils.js'
  import Button from '../Button.svelte'
  import { smbOutcomeText } from '../../api/smb-text'
  import Dialog from '../Dialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import IconButton from '../IconButton.svelte'
  import ListItem from '../ListItem.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import Switch from '../Switch.svelte'
  import Select from '../Select.svelte'
  import TextField from '../TextField.svelte'

  const sharesQuery = createQuery(() => adminSharesQuery())
  const shares = $derived(sharesQuery.data ?? [])
  const loading = $derived(sharesQuery.isPending)
  const loadError = $derived(sharesQuery.error ? describeApiError(sharesQuery.error, t('folder_share.could_not_load_share_list')) : null)

  /** A bad host path refuses with `admin.share_rejected` and a `reason`
   *  argument (missing, unreadable, an unsupported filesystem);
   *  `describeApiError` renders the keyed message with that reason filled
   *  in. This used to reverse the server's English sentences back into keys
   *  by substring, which meant a one-word copy edit on the server silently
   *  dropped the screen to its generic fallback. */
  function describeError(err: unknown, fallback: string): string {
    if (err instanceof ApiError && err.code === 'fs.not_found') return t('common.share_no_longer_exists')
    return describeApiError(err, fallback)
  }

  // What the SMB republish that every write here triggers did. It is reported
  // beside the list rather than swallowed: a share saved with the sidecar
  // stopped answered a clean success, and "saved here, not applied over
  // there" showed up only on the health page whenever somebody next looked.
  let smbNote = $state<string | null>(null)

  function noteSMB(share: { smb?: SMBOutcome }): void {
    smbNote = smbOutcomeText(share.smb)
  }

  // ── the three backends ──

  // The chooser's options, and the word a row shows for a backend it already
  // has. Both read the same catalogue keys, so the label on the picker and
  // the label on the list can never say different things about one backend.
  /* i18n */ 'folder_share.backend_local'
  /* i18n */ 'folder_share.backend_s3'
  /* i18n */ 'folder_share.backend_veracrypt'
  function backendLabel(backend: ShareBackend): string {
    switch (backend) {
      case 's3':
        return t('folder_share.backend_s3')
      case 'veracrypt':
        return t('folder_share.backend_veracrypt')
      default:
        return t('folder_share.backend_local')
    }
  }

  const backendOptions = $derived([
    { value: 'local', text: t('folder_share.backend_local') },
    { value: 's3', text: t('folder_share.backend_s3') },
    { value: 'veracrypt', text: t('folder_share.backend_veracrypt') }
  ])

  // One form's worth of backend fields, held apart from the dialog that shows
  // them so the add and edit dialogs can each own a copy without either
  // repeating the shape.
  interface BackendForm {
    hostPath: string
    s3Endpoint: string
    s3Region: string
    s3Bucket: string
    s3Prefix: string
    s3AccessKey: string
    s3SecretKey: string
    s3PathStyle: boolean
    // Whether the operator touched the switch. An untouched switch is left
    // out of a patch entirely, since sending its default would silently
    // change a setting nobody looked at.
    s3PathStyleTouched: boolean
    vaultContainer: string
    vaultPassword: string
    vaultCreate: boolean
    vaultSizeMiB: string
    vaultPIM: string
  }

  function emptyBackendForm(): BackendForm {
    return {
      hostPath: '',
      s3Endpoint: '',
      s3Region: 'us-east-1',
      s3Bucket: '',
      s3Prefix: '',
      s3AccessKey: '',
      s3SecretKey: '',
      // On by default because a self-hosted store almost always needs it: a
      // bucket addressed as a subdomain needs DNS entries a MinIO on a
      // private network does not have.
      s3PathStyle: true,
      s3PathStyleTouched: false,
      vaultContainer: '',
      vaultPassword: '',
      vaultCreate: true,
      vaultSizeMiB: '256',
      vaultPIM: ''
    }
  }

  /** The smallest container the server will make, in MiB. Below this a FAT32
   *  filesystem has too few clusters for the format, which the server also
   *  refuses; refusing it here too means the operator hears about it without
   *  a round trip. */
  const minVaultSizeMiB = 16

  /** The ceiling `vault.Config.PIM` accepts. The multiplier becomes an
   *  iteration count on every open, so a value here that the server would
   *  refuse is worth catching before the round trip. */
  const maxVaultPIM = 10000

  /** What is wrong with `form` for `backend`, or null when nothing is.
   *
   *  It mirrors the server's own refusals so a missing field is named before
   *  a request goes out. The server still checks: this is a courtesy, not the
   *  trust boundary. */
  function validateBackend(backend: ShareBackend, form: BackendForm): string | null {
    if (backend === 'local') {
      return form.hostPath.trim() === '' ? t('folder_share.enter_server_path') : null
    }
    if (backend === 's3') {
      if (form.s3Endpoint.trim() === '') return t('folder_share.enter_endpoint')
      if (form.s3Region.trim() === '') return t('folder_share.enter_region')
      if (form.s3Bucket.trim() === '') return t('folder_share.enter_bucket')
      if (form.s3AccessKey.trim() === '') return t('folder_share.enter_access_key')
      if (form.s3SecretKey === '') return t('folder_share.enter_secret_key')
      return null
    }
    if (form.vaultContainer.trim() === '') return t('folder_share.enter_container_path')
    if (form.vaultPassword === '') return t('folder_share.enter_password')
    if (form.vaultCreate && !(Number(form.vaultSizeMiB) >= minVaultSizeMiB)) {
      return t('folder_share.size_at_least', { min: String(minVaultSizeMiB) })
    }
    if (form.vaultPIM !== '' && !(Number(form.vaultPIM) >= 0 && Number(form.vaultPIM) <= maxVaultPIM)) {
      return t('folder_share.pim_at_most', { max: String(maxVaultPIM) })
    }
    return null
  }

  function s3ConfigOf(form: BackendForm): ShareS3Config {
    return {
      endpoint: form.s3Endpoint.trim(),
      region: form.s3Region.trim(),
      bucket: form.s3Bucket.trim(),
      prefix: form.s3Prefix.trim(),
      access_key_id: form.s3AccessKey.trim(),
      secret_access_key: form.s3SecretKey,
      path_style: form.s3PathStyle
    }
  }

  function vaultConfigOf(form: BackendForm): ShareVeracryptConfig {
    const cfg: ShareVeracryptConfig = {
      container: form.vaultContainer.trim(),
      password: form.vaultPassword,
      create: form.vaultCreate
    }
    // Only meaningful when creating, and the server refuses it otherwise.
    if (form.vaultCreate) cfg.size_mib = Number(form.vaultSizeMiB)
    if (form.vaultPIM !== '') cfg.pim = Number(form.vaultPIM)
    return cfg
  }

  // ── add share ──

  let addOpen = $state(false)
  let addName = $state('')
  let addBackend = $state<ShareBackend>('local')
  let addForm = $state<BackendForm>(emptyBackendForm())
  let addValidation = $state<string | null>(null)

  const addMut = createMutation(() => adminShareMutation())
  const adding = $derived(addMut.isPending)
  const addError = $derived.by(() => {
    if (addValidation) return addValidation
    return addMut.error ? describeError(addMut.error, t('common.could_not_add_folder')) : null
  })

  function openAdd(): void {
    addName = ''
    addBackend = 'local'
    addForm = emptyBackendForm()
    addValidation = null
    addMut.reset()
    addOpen = true
  }

  function closeAdd(): void {
    if (adding) return
    addOpen = false
  }

  function createReqOf(): CreateShareReq {
    const req: CreateShareReq = { name: addName.trim(), backend: addBackend }
    // Exactly the one object the chosen backend takes. The server refuses a
    // request carrying another backend's fields, so sending a spare one turns
    // a valid form into a refusal.
    if (addBackend === 'local') req.host = addForm.hostPath.trim()
    if (addBackend === 's3') req.s3 = s3ConfigOf(addForm)
    if (addBackend === 'veracrypt') req.veracrypt = vaultConfigOf(addForm)
    return req
  }

  async function submitAdd(): Promise<void> {
    addValidation = null
    if (addName.trim() === '') {
      addValidation = t('folder_share.enter_name')
      return
    }
    addValidation = validateBackend(addBackend, addForm)
    if (addValidation) return
    try {
      const created = (await addMut.mutateAsync({
        kind: 'create',
        req: createReqOf()
      })) as AdminShare
      noteSMB(created)
      addOpen = false
    } catch {
      // addError above reads the failure straight off addMut.error
    }
  }

  // ── edit share ──

  let editTarget = $state<AdminShare | null>(null)
  let editName = $state('')
  let editForm = $state<BackendForm>(emptyBackendForm())
  let editValidation = $state<string | null>(null)

  const editMut = createMutation(() => adminShareMutation())
  const editing = $derived(editMut.isPending)
  const editError = $derived.by(() => {
    if (editValidation) return editValidation
    return editMut.error ? describeError(editMut.error, t('common.could_not_save_change')) : null
  })

  function openEdit(s: AdminShare): void {
    editTarget = s
    editName = s.name
    editForm = emptyBackendForm()
    // A local share's path is prefilled, so a rename does not mean retyping a
    // disk path from memory. The other two backends are not: the response
    // carries their location as one redacted sentence rather than as fields,
    // by design, since the alternative is the presentation tier parsing a
    // backend's own config. The dialog shows that sentence and treats an
    // empty field as "leave this alone", which is what the server's patch
    // already means.
    editForm.hostPath = s.host
    // The create form's suggested region is a default for a new share, not a
    // value this share has. Leaving it filled in would make every save
    // rewrite the region to a guess.
    editForm.s3Region = ''
    editValidation = null
    editMut.reset()
  }

  function closeEdit(): void {
    if (editing) return
    editTarget = null
  }

  /** The s3 fields the operator actually filled in, or null when they filled
   *  none: an empty object would still be a patch, and the server reads a
   *  present-but-empty credential as an attempt to clear it. */
  function s3PatchOf(form: BackendForm): ShareS3Config | null {
    const cfg: ShareS3Config = {}
    if (form.s3Endpoint.trim() !== '') cfg.endpoint = form.s3Endpoint.trim()
    if (form.s3Region.trim() !== '') cfg.region = form.s3Region.trim()
    if (form.s3Bucket.trim() !== '') cfg.bucket = form.s3Bucket.trim()
    if (form.s3Prefix.trim() !== '') cfg.prefix = form.s3Prefix.trim()
    if (form.s3AccessKey.trim() !== '') cfg.access_key_id = form.s3AccessKey.trim()
    if (form.s3SecretKey !== '') cfg.secret_access_key = form.s3SecretKey
    if (form.s3PathStyleTouched) cfg.path_style = form.s3PathStyle
    return Object.keys(cfg).length > 0 ? cfg : null
  }

  /** The same for a container. `create` and `size_mib` are never patched: the
   *  container is made once, and the server refuses either on a patch. */
  function vaultPatchOf(form: BackendForm): ShareVeracryptConfig | null {
    const cfg: ShareVeracryptConfig = {}
    if (form.vaultContainer.trim() !== '') cfg.container = form.vaultContainer.trim()
    if (form.vaultPassword !== '') cfg.password = form.vaultPassword
    if (form.vaultPIM !== '') cfg.pim = Number(form.vaultPIM)
    return Object.keys(cfg).length > 0 ? cfg : null
  }

  async function submitEdit(): Promise<void> {
    if (!editTarget) return
    editValidation = null
    if (editName.trim() === '') {
      editValidation = t('folder_share.enter_name')
      return
    }
    if (editTarget.backend === 'local' && editForm.hostPath.trim() === '') {
      editValidation = t('folder_share.enter_server_path')
      return
    }

    // Only what changed. Sending a value back unaltered would re-register the
    // share for no reason, which drops and re-opens its root.
    const patch: UpdateShareReq = { name: editName.trim() }
    if (editTarget.backend === 'local') {
      if (editForm.hostPath.trim() !== editTarget.host) patch.host = editForm.hostPath.trim()
    } else if (editTarget.backend === 's3') {
      const s3 = s3PatchOf(editForm)
      if (s3) patch.s3 = s3
    } else {
      const vc = vaultPatchOf(editForm)
      if (vc) patch.veracrypt = vc
    }

    try {
      const updated = (await editMut.mutateAsync({ kind: 'update', id: editTarget.id, patch })) as AdminShare
      noteSMB(updated)
      editTarget = null
    } catch {
      // editError above reads the failure straight off editMut.error
    }
  }

  // ── trash toggle ──

  const trashMut = createMutation(() => adminShareMutation())
  const trashTogglingId = $derived(trashMut.isPending && trashMut.variables?.kind === 'update' ? trashMut.variables.id : null)
  const trashToggleError = $derived(trashMut.error ? describeError(trashMut.error, t('folder_share.could_not_change_trash_setting')) : null)

  function toggleTrash(s: AdminShare, enabled: boolean): void {
    trashMut.mutate({ kind: 'update', id: s.id, patch: { trash_enabled: enabled } })
  }

  // ── retry a broken share ──

  // A share whose backing folder is not there is listed, marked, with the
  // three things an administrator can do about it: retry, because the usual
  // repair is a remount that changes nothing about the share; edit, because
  // the folder may have moved; remove, because it may be gone for good.
  const retryMut = createMutation(() => adminShareMutation())
  const retryingId = $derived(retryMut.isPending && retryMut.variables?.kind === 'retry' ? retryMut.variables.id : null)
  const retryError = $derived(
    // Still broken. The message says why this attempt failed, which is not
    // necessarily the reason the row is already showing.
    retryMut.error ? describeError(retryMut.error, t('folder_share.the_folder_is_still_unavailable')) : null
  )

  async function retry(s: AdminShare): Promise<void> {
    try {
      const healed = (await retryMut.mutateAsync({ kind: 'retry', id: s.id })) as AdminShare
      noteSMB(healed)
    } catch {
      // retryError above reads the failure straight off retryMut.error
    }
  }

  // The catalogue renders the sentence; the server sends the token.
  /* i18n */ 'folder_share.broken_missing'
  /* i18n */ 'folder_share.broken_unreadable'
  /* i18n */ 'folder_share.broken_unavailable'
  /* i18n */ 'folder_share.broken_passphrase'
  /* i18n */ 'folder_share.broken_container_corrupt'
  /* i18n */ 'folder_share.broken_container_filesystem'
  /* i18n */ 'folder_share.broken_container_unsupported'
  function brokenText(reason: string): string {
    switch (reason) {
      case 'missing':
        return t('folder_share.broken_missing')
      case 'unreadable':
        return t('folder_share.broken_unreadable')
      case 'passphrase':
        return t('folder_share.broken_passphrase')
      case 'container_corrupt':
        return t('folder_share.broken_container_corrupt')
      case 'container_filesystem':
        return t('folder_share.broken_container_filesystem')
      case 'container_unsupported':
        return t('folder_share.broken_container_unsupported')
      default:
        return t('folder_share.broken_unavailable')
    }
  }

  // ── remove share ──

  let deleteTarget = $state<AdminShare | null>(null)

  const deleteMut = createMutation(() => adminShareMutation())
  const deleting = $derived(deleteMut.isPending)
  const deleteError = $derived(deleteMut.error ? describeError(deleteMut.error, t('common.could_not_remove')) : null)

  function askDelete(s: AdminShare): void {
    deleteMut.reset()
    deleteTarget = s
  }

  function closeDelete(): void {
    if (deleting) return
    deleteTarget = null
  }

  async function confirmDelete(): Promise<void> {
    if (!deleteTarget) return
    try {
      const removed = (await deleteMut.mutateAsync({ kind: 'delete', id: deleteTarget.id })) as { smb?: SMBOutcome }
      smbNote = smbOutcomeText(removed.smb)
      deleteTarget = null
    } catch {
      // deleteError above reads the failure straight off deleteMut.error
    }
  }

  // ── encryption ──
  //
  // Content encryption is opt-in per share and off by default, in the
  // rclone `crypt` format: what ends up on disk is byte-for-byte what
  // `rclone mount` with a crypt remote already reads and writes, so an
  // encrypted share needs no client of ours at all. The server never sees
  // the passphrase or the key derived from it, only ciphertext plus a
  // public, non-secret salt and a small verifier that catches a mistyped
  // passphrase before it silently writes under the wrong key. Enabling and
  // disabling both require an empty share, and this screen has no way to
  // know that itself, so the guard is only ever surfaced by attempting the
  // change and rendering whatever the server refuses it with.
  const encQuery = createQuery(() => ({
    queryKey: ['share-encryption'] as const,
    queryFn: () => api.shareEncryptionList()
  }))
  const encByShare = $derived(new Map((encQuery.data?.shares ?? []).map((e) => [e.share, e] as const)))
  const encLoadError = $derived(
    encQuery.error ? describeApiError(encQuery.error, t('encryption.could_not_load_status')) : null
  )

  /** This screen's own live region for encryption changes and salt copies,
   *  same reasoning as `SmbSection`'s: a per-route Snackbar goes silent on
   *  navigation. */
  let encAnnouncement = $state('')

  async function copySalt(salt: string, name: string): Promise<void> {
    if (!salt) return
    try {
      await navigator.clipboard.writeText(salt)
      encAnnouncement = t('encryption.salt_copied', { name })
    } catch {
      // clipboard API unavailable: the salt is still selectable, user-select: all
    }
  }

  // ── enable ──

  let encEnableTarget = $state<AdminShare | null>(null)
  let encPassphrase = $state('')
  let encPassphraseConfirm = $state('')
  let encGenerating = $state(false)
  let encGenerateError = $state<string | null>(null)

  const encEnableMut = createMutation(() => ({
    mutationFn: (req: { id: number; scheme: string; salt: string; verifier: string }) =>
      api.adminEnableShareEncryption(req.id, { scheme: req.scheme, salt: req.salt, verifier: req.verifier })
  }))
  const encMismatch = $derived(encPassphraseConfirm.length > 0 && encPassphrase !== encPassphraseConfirm)
  const encEnableError = $derived.by(() => {
    if (encGenerateError) return encGenerateError
    return encEnableMut.error ? describeApiError(encEnableMut.error, t('encryption.could_not_enable')) : null
  })

  function openEncEnable(s: AdminShare): void {
    encEnableTarget = s
    encPassphrase = ''
    encPassphraseConfirm = ''
    encGenerateError = null
    encEnableMut.reset()
  }

  function closeEncEnable(): void {
    if (encGenerating || encEnableMut.isPending) return
    encEnableTarget = null
  }

  async function submitEncEnable(): Promise<void> {
    const target = encEnableTarget
    if (!target || !encPassphrase || encMismatch) return
    encGenerateError = null
    encGenerating = true
    // The passphrase and the key derived from it live only in these local
    // variables: the wire only ever carries the salt (public) and the
    // verifier (an encrypted probe, not the key itself). keys is zeroed in
    // the finally block below once it is no longer needed.
    let keys: DerivedKeys | null = null
    try {
      const salt = generateSalt()
      keys = await deriveKeys(encPassphrase, salt)
      const verifier = await makeVerifier(keys)
      await encEnableMut.mutateAsync({ id: target.id, scheme: 'rclone-crypt-v1', salt, verifier })
      await queryClient.invalidateQueries({ queryKey: ['share-encryption'] })
      // Distinct from the admin query cache just invalidated above:
      // `encryptionForLabel`'s own cache (crypto/encrypted-shares.ts) is
      // what every upload and download decision actually reads, and it is
      // never refetched on its own. Without this, a share enabled here
      // stays invisible to the download/upload path until something else
      // happens to invalidate it (e.g. a reload), so an admin who just
      // turned encryption on and switches to the file browser would see
      // plaintext still flow.
      invalidateEncryptedShares()
      encAnnouncement = t('encryption.enabled_for', { name: target.name })
      encEnableTarget = null
      encPassphrase = ''
      encPassphraseConfirm = ''
    } catch (err) {
      if (!(err instanceof ApiError)) encGenerateError = t('encryption.could_not_create_key')
      // an ApiError from the mutation is already on encEnableMut.error and
      // rendered by encEnableError above
    } finally {
      encGenerating = false
      if (keys) clean(keys.dataKey)
    }
  }

  // ── disable ──

  let encDisableTarget = $state<AdminShare | null>(null)
  const encDisableMut = createMutation(() => ({
    mutationFn: (id: number) => api.adminDisableShareEncryption(id)
  }))
  const encDisableError = $derived(
    encDisableMut.error ? describeApiError(encDisableMut.error, t('encryption.could_not_disable')) : null
  )

  function openEncDisable(s: AdminShare): void {
    encDisableTarget = s
    encDisableMut.reset()
  }

  function closeEncDisable(): void {
    if (encDisableMut.isPending) return
    encDisableTarget = null
  }

  async function confirmEncDisable(): Promise<void> {
    const target = encDisableTarget
    if (!target) return
    try {
      await encDisableMut.mutateAsync(target.id)
      await queryClient.invalidateQueries({ queryKey: ['share-encryption'] })
      invalidateEncryptedShares()
      encAnnouncement = t('encryption.disabled_for', { name: target.name })
      encDisableTarget = null
    } catch {
      // encDisableError above reads the failure straight off encDisableMut.error
    }
  }
</script>

<section class="sc-shares">
  <h2>{t('folder_share.folder_shares')}</h2>
  <p class="sc-shares__hint">
    {t('folder_share.registers_real_folder_on_server')}
  </p>

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-shares__error">{loadError}</p>
  {:else}
    {#if shares.length === 0}
      <div class="sc-shares__empty">
        <Icon icon={icons['folder-tree']} size={28} />
        <p>{t('folder_share.no_shares_registered_add_folder')}</p>
      </div>
    {:else}
      <ul class="sc-shares__list">
        {#each shares as s (s.id)}
          <li>
            <ListItem>
              {#snippet leading()}<Icon icon={icons.folder} size={20} />{/snippet}
              {#snippet headline()}
                <span class="sc-shares__name">{s.name}</span>
                {#if s.backend !== 'local'}
                  <!-- Only the two backends that are not a folder on this
                       server are marked. Naming the ordinary case too would
                       put a chip on every row and distinguish nothing. -->
                  <span class="sc-shares__backend" data-testid="share-backend">{backendLabel(s.backend)}</span>
                {/if}
              {/snippet}
              {#snippet supporting()}
                <!-- Where the share serves from, which only an administrator
                     sees: these routes are administrator-only and
                     session-only. It is on the row because an operator with
                     several folders cannot tell them apart by name alone, and
                     because a location nobody can read is one nobody can
                     correct. Never a credential: the server builds this
                     string from the backend's configuration alone. -->
                <span class="sc-shares__path" data-testid="share-source">{s.source}</span>
                {#if s.broken_reason}
                  <!-- The row stays, with the reason on it. A share dropped
                       from the list is indistinguishable from one somebody
                       deleted, which is the worst thing a missing disk can
                       look like. -->
                  <span class="sc-shares__broken">{brokenText(s.broken_reason)}</span>
                {/if}
                {#if encQuery.data}
                  <!-- The toggle sits here rather than beside the switch: it
                       is offered only on an empty share, and a control that
                       appears on some rows and not others pushes the switch
                       and the icon buttons out of line down the list. It also
                       reads better next to the state it changes. -->
                  <span class="sc-shares__enc" data-testid="share-encryption">
                    {#if encByShare.get(s.id)}
                      <span class="sc-shares__enc-note">
                        <Icon icon={icons.lock} size={14} />
                        {t('encryption.encrypted_note')}
                      </span>
                      <Button
                        variant="text"
                        ariaLabel={t('encryption.disable_title', { name: s.name })}
                        onclick={() => openEncDisable(s)}
                      >
                        {t('encryption.disable')}
                      </Button>
                    {:else if s.empty}
                      <Button
                        variant="text"
                        ariaLabel={t('encryption.enable_title', { name: s.name })}
                        onclick={() => openEncEnable(s)}
                      >
                        {t('encryption.enable')}
                      </Button>
                    {/if}
                  </span>
                {/if}
                {#if encByShare.get(s.id)}
                  <!-- Persistent, not a one-time reveal: the salt is public
                       by construction and has to stay readable for as long
                       as the share is encrypted, since it is the value an
                       admin types into rclone as `password2`. -->
                  <span class="sc-shares__enc-salt-row">
                    <span class="sc-shares__enc-salt-label">{t('encryption.salt_label')}</span>
                    <code class="sc-shares__enc-salt" data-testid="share-encryption-salt">{encByShare.get(s.id)?.salt}</code>
                    <Button
                      variant="text"
                      ariaLabel={t('encryption.copy_salt', { name: s.name })}
                      onclick={() => copySalt(encByShare.get(s.id)?.salt ?? '', s.name)}
                    >
                      {t('common.copy')}
                    </Button>
                  </span>
                {/if}
              {/snippet}
              {#snippet trailing()}
                <!-- The visible word beside the switch is the short label; the
                     switch keeps the longer, per-share sentence as its own
                     accessible name, since a screen reader needs to know which
                     share's trash setting this is. -->
                <span class="sc-shares__trash" title={trashTogglingId === s.id ? t('folder_share.applying') : undefined}>
                  <span class="sc-shares__trash-label">{t('folder_share.use_trash')}</span>
                  <Switch
                    checked={s.trash_enabled}
                    label={t('folder_share.trash', { name: s.name })}
                    showLabel={false}
                    onchange={(checked) => toggleTrash(s, checked)}
                  />
                </span>
                {#if s.broken_reason}
                  <Button variant="tonal" onclick={() => retry(s)} loading={retryingId === s.id}>
                    {t('folder_share.retry')}
                  </Button>
                {/if}
                <IconButton label={t('common.edit', { name: s.name })} onclick={() => openEdit(s)}>
                  <Icon icon={icons.rename} size={18} />
                </IconButton>
                <IconButton label={t('common.remove', { name: s.name })} onclick={() => askDelete(s)}>
                  <Icon icon={icons.delete} size={18} />
                </IconButton>
              {/snippet}
            </ListItem>
          </li>
        {/each}
      </ul>
    {/if}
    {#if trashToggleError}<p class="sc-shares__error" role="alert">{trashToggleError}</p>{/if}
    {#if retryError}<p class="sc-shares__error" role="alert">{retryError}</p>{/if}
    {#if smbNote}<p class="sc-shares__smb" role="status">{smbNote}</p>{/if}
    {#if encLoadError}<p class="sc-shares__error" role="alert">{encLoadError}</p>{/if}
    <p class="sc-shares__enc-announce" aria-live="polite">{encAnnouncement}</p>

    <Button variant="tonal" onclick={openAdd}>
      {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
      {t('common.add_folder')}
    </Button>
  {/if}
</section>

<!-- The backend fields for one form. Shared by the add and edit dialogs so
     the two cannot drift apart on what a bucket or a container needs; each
     passes its own form state and says whether it is a create, which is the
     only thing that differs (a create requires every field and may make a
     container; a patch treats an empty field as "leave it alone"). -->
{#snippet s3Fields(form: BackendForm, creating: boolean)}
  <TextField
    label={t('folder_share.s3_endpoint')}
    bind:value={form.s3Endpoint}
    placeholder={t('folder_share.e_g_s3_endpoint')}
    autocomplete="off"
  />
  <TextField
    label={t('folder_share.s3_bucket')}
    bind:value={form.s3Bucket}
    placeholder={t('folder_share.e_g_s3_bucket')}
    autocomplete="off"
  />
  <TextField label={t('folder_share.s3_region')} bind:value={form.s3Region} autocomplete="off" />
  <TextField
    label={t('folder_share.s3_prefix')}
    bind:value={form.s3Prefix}
    placeholder={t('folder_share.e_g_s3_prefix')}
    autocomplete="off"
  />
  <TextField label={t('folder_share.s3_access_key')} bind:value={form.s3AccessKey} autocomplete="off" />
  <TextField
    label={t('folder_share.s3_secret_key')}
    bind:value={form.s3SecretKey}
    type="password"
    autocomplete="off"
  />
  {#if !creating}
    <p class="sc-shares__field-hint">{t('folder_share.keep_stored_credential')}</p>
  {/if}
  <Switch
    checked={form.s3PathStyle}
    label={t('folder_share.s3_path_style')}
    onchange={(checked) => {
      form.s3PathStyle = checked
      form.s3PathStyleTouched = true
    }}
  />
  <p class="sc-shares__field-hint">{t('folder_share.s3_hint')}</p>
{/snippet}

{#snippet vaultFields(form: BackendForm, creating: boolean)}
  <TextField
    label={t('folder_share.vault_container')}
    bind:value={form.vaultContainer}
    placeholder={t('folder_share.e_g_vault_container')}
    autocomplete="off"
  />
  <TextField
    label={t('folder_share.vault_pim')}
    bind:value={form.vaultPIM}
    type="number"
    min={0}
    max={maxVaultPIM}
  />
  <p class="sc-shares__field-hint">{t('folder_share.vault_pim_hint')}</p>
  <TextField
    label={t('folder_share.vault_password')}
    bind:value={form.vaultPassword}
    type="password"
    autocomplete="off"
  />
  {#if creating}
    <Switch
      checked={form.vaultCreate}
      label={t('folder_share.vault_create')}
      onchange={(checked) => (form.vaultCreate = checked)}
    />
    {#if form.vaultCreate}
      <TextField
        label={t('folder_share.vault_size')}
        bind:value={form.vaultSizeMiB}
        type="number"
        min={minVaultSizeMiB}
      />
    {/if}
  {:else}
    <p class="sc-shares__field-hint">{t('folder_share.keep_stored_credential')}</p>
  {/if}
  <p class="sc-shares__field-hint">{t('folder_share.vault_hint')}</p>
{/snippet}

<Dialog open={addOpen} title={t('common.add_folder')} onclose={closeAdd}>
  <form class="sc-shares__form" onsubmit={(e) => (e.preventDefault(), submitAdd())}>
    <TextField label={t('common.name')} bind:value={addName} placeholder={t('folder_share.e_g_photos')} autocomplete="off" />
    <!-- First, because every field under it depends on the answer. A native
         select rather than three panels: the choice is one of a closed set,
         and the platform control is the one every keyboard and screen reader
         already knows. -->
    <Select
      label={t('folder_share.backend')}
      options={backendOptions}
      bind:value={addBackend}
      testid="share-backend-select"
    />
    {#if addBackend === 'local'}
      <TextField
        label={t('folder_share.server_path')}
        bind:value={addForm.hostPath}
        placeholder={t('folder_share.e_g_srv_photos')}
        autocomplete="off"
      />
      <p class="sc-shares__field-hint">{t('folder_share.enter_path_folder_already_exists')}</p>
    {:else if addBackend === 's3'}
      {@render s3Fields(addForm, true)}
    {:else}
      {@render vaultFields(addForm, true)}
    {/if}
    {#if addError}<p class="sc-shares__error" role="alert">{addError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeAdd} disabled={adding}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submitAdd} loading={adding}>{t('common.add')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!editTarget} title={t('folder_share.edit_folder')} onclose={closeEdit}>
  {#if editTarget}
    <form class="sc-shares__form" onsubmit={(e) => (e.preventDefault(), submitEdit())}>
      <TextField label={t('common.name')} bind:value={editName} autocomplete="off" />
      <!-- Stated, not offered. A share's backend is fixed at creation: every
           grant, share link and cached identity references data the old one
           holds, so the server refuses a change. Saying which it is, and that
           it cannot move, is more use than a disabled control. -->
      <p class="sc-shares__field-hint">
        {t('folder_share.backend_fixed', { backend: backendLabel(editTarget.backend) })}
      </p>
      {#if editTarget.backend === 'local'}
        <TextField label={t('folder_share.server_path')} bind:value={editForm.hostPath} autocomplete="off" />
      {:else}
        <p class="sc-shares__field-hint" data-testid="edit-share-source">
          {t('folder_share.current_location', { source: editTarget.source })}
        </p>
        {#if editTarget.backend === 's3'}
          {@render s3Fields(editForm, false)}
        {:else}
          {@render vaultFields(editForm, false)}
        {/if}
      {/if}
      {#if editError}<p class="sc-shares__error" role="alert">{editError}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeEdit} disabled={editing}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submitEdit} loading={editing}>{t('common.save')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!deleteTarget} title={t('folder_share.remove_share')} onclose={closeDelete}>
  <p>
    {t('folder_share.removes_every_user_permission_granted', {
      name: deleteTarget?.name ?? ''
    })}
  </p>
  {#if deleteError}<p class="sc-shares__error" role="alert">{deleteError}</p>{/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeDelete} disabled={deleting}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmDelete} loading={deleting}>{t('common.remove_2')}</Button>
  {/snippet}
</Dialog>

<Dialog
  open={!!encEnableTarget}
  title={t('encryption.enable_title', { name: encEnableTarget?.name ?? '' })}
  onclose={closeEncEnable}
>
  {#if encEnableTarget}
    <div class="sc-shares__enc-body">
      <p>{t('encryption.enable_hint')}</p>
      <p class="sc-shares__enc-warning">{t('encryption.passphrase_warning')}</p>
      <p class="sc-shares__enc-hint">{t('encryption.verifier_note')}</p>
      <form class="sc-shares__form" onsubmit={(e) => (e.preventDefault(), submitEncEnable())}>
        <TextField
          type="password"
          label={t('encryption.passphrase')}
          bind:value={encPassphrase}
          autocomplete="new-password"
        />
        <TextField
          type="password"
          label={t('encryption.confirm_passphrase')}
          bind:value={encPassphraseConfirm}
          error={encMismatch ? t('encryption.passphrases_do_not_match') : null}
          autocomplete="new-password"
        />
        {#if encEnableError}<p class="sc-shares__error" role="alert">{encEnableError}</p>{/if}
      </form>
    </div>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeEncEnable} disabled={encGenerating || encEnableMut.isPending}>
      {t('common.cancel')}
    </Button>
    <Button
      variant="filled"
      onclick={submitEncEnable}
      loading={encGenerating || encEnableMut.isPending}
      disabled={!encPassphrase || !encPassphraseConfirm || encMismatch}
    >
      {t('encryption.enable')}
    </Button>
  {/snippet}
</Dialog>

<Dialog
  open={!!encDisableTarget}
  title={t('encryption.disable_title', { name: encDisableTarget?.name ?? '' })}
  onclose={closeEncDisable}
>
  <p>{t('encryption.disable_hint')}</p>
  {#if encDisableError}<p class="sc-shares__error" role="alert">{encDisableError}</p>{/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeEncDisable} disabled={encDisableMut.isPending}>
      {t('common.cancel')}
    </Button>
    <Button variant="filled" onclick={confirmEncDisable} loading={encDisableMut.isPending}>
      {t('encryption.disable')}
    </Button>
  {/snippet}
</Dialog>

<style>
  .sc-shares {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .sc-shares h2 {
    margin: 0;
    @apply --m3-title-large;
  }
  .sc-shares__hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-shares__empty {
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
  .sc-shares__empty p {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-shares__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-shares__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  /* The name yields width, not the chip beside it: a Korean share name has a
     break opportunity after every syllable, so squeezed it wraps character by
     character instead of ellipsizing (same as the user and app-password lists). */
  .sc-shares__name {
    overflow: hidden;
    min-width: 0;
    flex: 1 1 auto;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
    color: var(--m3c-on-surface);
  }
  /* The backend, as a word beside the name. Not colour alone: a chip that
     only differed by background would say nothing to a reader who cannot see
     the difference, and there are three backends rather than two states. */
  .sc-shares__backend {
    flex: 0 0 auto;
    margin-left: 8px;
    padding: 0 8px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
    white-space: nowrap;
    @apply --m3-label-small;
  }
  /* The error colour, plus the word itself: a row marked only by colour says
     nothing to a reader who cannot see the difference. */
  .sc-shares__smb {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface);
    @apply --m3-body-small;
  }
  .sc-shares__path {
    display: block;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    font-family: var(--sc-font-mono, monospace);
    overflow-wrap: anywhere;
  }
  .sc-shares__broken {
    display: block;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  /* Same shape as `.sc-shares__broken`, neutral rather than error-coloured:
     this describes what the setting does, not something wrong. */
  .sc-shares__enc-note {
    display: flex;
    align-items: center;
    gap: 4px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* The state and the button that changes it read as one line, and wrap
     together on a phone rather than the button dropping alone. */
  .sc-shares__enc {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
  }
  /* Same layout and token styling as `.sc-webdav__token-row` /
     `.sc-webdav__token` (`WebdavSection.svelte`): a labelled value plus a
     copy button is the one idiom this tree already has for "the user must
     type this into another program". */
  .sc-shares__enc-salt-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-block: 8px;
  }
  .sc-shares__enc-salt-label {
    color: var(--m3c-on-surface-variant);
    white-space: nowrap;
    @apply --m3-body-small;
  }
  .sc-shares__enc-salt {
    padding: 8px 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    @apply --m3-body-medium;
    overflow-wrap: anywhere;
    user-select: all;
  }
  /* Every block in this dialog zeroes its own margins, which left the
     paragraphs flush against each other and the first field's floating label
     drawn over the line of text above it. One rhythm on the wrapper instead
     of four sets of margins that have to agree. */
  .sc-shares__enc-body {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .sc-shares__enc-body p {
    margin: 0;
  }
  /* Same colours as `.sc-admin-section__warning` (`ServerSettingsSection.svelte`):
     a passphrase this server can never recover gets the loud treatment, not
     a tooltip. */
  .sc-shares__enc-warning {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
  .sc-shares__enc-hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-shares__enc-announce {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* `ListItem`'s trailing group has no gap of its own: every other caller puts
     only icon buttons there, which carry their own padding. This one holds a
     chip and a labelled switch as well, so it needs one. It replaces the
     margin `.sc-shares__trash` used to carry for the same reason, which only
     ever spaced the one thing that came after it. */
  .sc-shares__list :global(.sc-list-item__trailing) {
    gap: 8px;
  }
  .sc-shares__trash {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .sc-shares__trash-label {
    color: var(--m3c-on-surface-variant);
    white-space: nowrap;
    @apply --m3-label-large;
  }
  /* The trailing group wraps (`ListItem`'s own rule), so on a phone it sits
     on its own line under the name with the full card width to spend. The
     switch alone reads as an unlabelled toggle there, so the word stays. */
  .sc-shares__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-shares__form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: min(360px, 80vw);
  }
  .sc-shares__field-hint {
    margin: calc(-1 * 8px) 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
</style>
