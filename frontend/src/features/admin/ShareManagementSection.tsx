import { useMemo } from 'react'
import type { Dispatch, SetStateAction } from 'react'
import { useShareManagementState, type BackendForm, emptyBackendForm } from './hooks/share-management-state'
import { useI18n } from '../../hooks/use-i18n'
import { describeApiError } from '../../api/error-text'
import { deriveKeys, generateSalt, makeVerifier, unlock, type DerivedKeys } from '../../lib/crypto/e2ee'
import { clean } from '@noble/ciphers/utils.js'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { Icon } from '../../ui/Icon'
import { ShareManagementList } from './ShareManagementList'
import { Switch } from '../../ui/Switch'
import { pickPath } from '../files/PathPickerDialog'
import { ProgressCircular } from '../../ui/ProgressCircular'
import { Select } from '../../ui/Select'
import { TextField } from '../../ui/TextField'
import * as styles from './ShareManagementSection.css'
import * as adminStyles from './admin.css'
import { ApiError } from '../../api/fetcher'
import {
  useAdminShares,
  useCreateShare,
  useDeleteShare,
  useDisableShareEncryption,
  useEnableShareEncryption,
  useRetryShare,
  useUpdateShare,
  type AdminShare,
  type CreateShareReq,
  type ShareBackend,
  type ShareS3Config,
  type ShareVeracryptConfig,
  type UpdateShareReq
} from './api'
import { useShareEncryptions } from '../shares/api'

const MIN_VAULT_SIZE = 16
const MAX_VAULT_SIZE = 1 << 20
const MAX_VAULT_PIM = 10000

type Translator = (key: string, params?: Record<string, string | number>) => string
function backendLabel(t: Translator, backend: ShareBackend): string {
  if (backend === 's3') return t('folder_share.backend_s3')
  if (backend === 'veracrypt') return t('folder_share.backend_veracrypt')
  return t('folder_share.backend_local')
}

function errorText(error: unknown, fallback: string, t: Translator): string {
  if (error instanceof ApiError && error.code === 'fs.not_found') return t('common.share_no_longer_exists')
  return describeApiError(error, fallback)
}

function validateBackend(t: Translator, backend: ShareBackend, form: BackendForm): string | null {
  if (backend === 'local') return form.hostPath.trim() ? null : t('folder_share.enter_server_path')
  if (backend === 's3') {
    if (!form.s3Endpoint.trim()) return t('folder_share.enter_endpoint')
    if (!form.s3Region.trim()) return t('folder_share.enter_region')
    if (!form.s3Bucket.trim()) return t('folder_share.enter_bucket')
    if (!form.s3AccessKey.trim()) return t('folder_share.enter_access_key')
    if (!form.s3SecretKey) return t('folder_share.enter_secret_key')
    return null
  }
  if (!form.vaultContainer.trim()) return t('folder_share.enter_container_path')
  if (!form.vaultPassword) return t('folder_share.enter_password')
  if (form.vaultCreate && !(Number(form.vaultSizeMiB) >= MIN_VAULT_SIZE))
    return t('folder_share.size_at_least', { min: String(MIN_VAULT_SIZE) })
  if (form.vaultCreate && Number(form.vaultSizeMiB) > MAX_VAULT_SIZE)
    return t('folder_share.size_at_most', { max: String(MAX_VAULT_SIZE) })
  if (form.vaultPIM !== '' && !(Number(form.vaultPIM) >= 0 && Number(form.vaultPIM) <= MAX_VAULT_PIM))
    return t('folder_share.pim_at_most', { max: String(MAX_VAULT_PIM) })
  return null
}

function s3Of(form: BackendForm): ShareS3Config {
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

function vaultOf(form: BackendForm): ShareVeracryptConfig {
  const config: ShareVeracryptConfig = {
    container: form.vaultContainer.trim(),
    password: form.vaultPassword,
    create: form.vaultCreate
  }
  if (form.vaultCreate) config.size_mib = Number(form.vaultSizeMiB)
  if (form.vaultPIM !== '') config.pim = Number(form.vaultPIM)
  return config
}

function s3PatchOf(form: BackendForm): ShareS3Config | null {
  const config: ShareS3Config = {}
  if (form.s3Endpoint.trim()) config.endpoint = form.s3Endpoint.trim()
  if (form.s3Region.trim()) config.region = form.s3Region.trim()
  if (form.s3Bucket.trim()) config.bucket = form.s3Bucket.trim()
  if (form.s3Prefix.trim()) config.prefix = form.s3Prefix.trim()
  if (form.s3AccessKey.trim()) config.access_key_id = form.s3AccessKey.trim()
  if (form.s3SecretKey) config.secret_access_key = form.s3SecretKey
  if (form.s3PathStyleTouched) config.path_style = form.s3PathStyle
  return Object.keys(config).length ? config : null
}

function vaultPatchOf(form: BackendForm): ShareVeracryptConfig | null {
  const config: ShareVeracryptConfig = {}
  if (form.vaultContainer.trim()) config.container = form.vaultContainer.trim()
  if (form.vaultPassword) config.password = form.vaultPassword
  if (form.vaultPIM !== '') config.pim = Number(form.vaultPIM)
  return Object.keys(config).length ? config : null
}

interface S3FieldsProps {
  form: BackendForm
  setForm: Dispatch<SetStateAction<BackendForm>>
  creating: boolean
}

function S3Fields({ form, setForm, creating }: S3FieldsProps) {
  const { t } = useI18n()
  const update = (key: keyof BackendForm, value: string | boolean) =>
    setForm((current) => ({ ...current, [key]: value }))
  return (
    <>
      <TextField
        label={t('folder_share.s3_endpoint')}
        placeholder={t('folder_share.e_g_s3_endpoint')}
        value={form.s3Endpoint}
        onValueChange={(value) => update('s3Endpoint', value)}
        autoComplete="off"
      />
      <p className={adminStyles.hint}>{t('folder_share.s3_endpoint_scheme_hint')}</p>
      <TextField
        label={t('folder_share.s3_bucket')}
        placeholder={t('folder_share.e_g_s3_bucket')}
        value={form.s3Bucket}
        onValueChange={(value) => update('s3Bucket', value)}
        autoComplete="off"
      />
      <TextField
        label={t('folder_share.s3_region')}
        value={form.s3Region}
        onValueChange={(value) => update('s3Region', value)}
        autoComplete="off"
      />
      <TextField
        label={t('folder_share.s3_prefix')}
        placeholder={t('folder_share.e_g_s3_prefix')}
        value={form.s3Prefix}
        onValueChange={(value) => update('s3Prefix', value)}
        autoComplete="off"
      />
      <TextField
        label={t('folder_share.s3_access_key')}
        value={form.s3AccessKey}
        onValueChange={(value) => update('s3AccessKey', value)}
        autoComplete="off"
      />
      <TextField
        label={t('folder_share.s3_secret_key')}
        value={form.s3SecretKey}
        onValueChange={(value) => update('s3SecretKey', value)}
        type="password"
        autoComplete="off"
      />
      {!creating ? <p className={adminStyles.hint}>{t('folder_share.keep_stored_credential')}</p> : null}
      <Switch
        checked={form.s3PathStyle}
        label={t('folder_share.s3_path_style')}
        onChange={(checked) => setForm((current) => ({ ...current, s3PathStyle: checked, s3PathStyleTouched: true }))}
      />
      <p className={adminStyles.hint}>{t('folder_share.s3_hint')}</p>
    </>
  )
}

interface VaultFieldsProps {
  form: BackendForm
  setForm: Dispatch<SetStateAction<BackendForm>>
  creating: boolean
  openPathPicker: (start: string, apply: (path: string) => void) => void
}

function VaultFields({ form, setForm, creating, openPathPicker }: VaultFieldsProps) {
  const { t } = useI18n()
  const update = (key: keyof BackendForm, value: string | boolean) =>
    setForm((current) => ({ ...current, [key]: value }))
  return (
    <>
      <div className={styles.pathRow}>
        <TextField
          className={styles.pathRowField}
          label={t('folder_share.vault_container')}
          placeholder={t('folder_share.e_g_vault_container')}
          value={form.vaultContainer}
          onValueChange={(value) => update('vaultContainer', value)}
          autoComplete="off"
        />
        <Button
          className={styles.pathRowButton}
          variant="outlined"
          icon={<Icon name="file" />}
          onClick={() => openPathPicker(form.vaultContainer, (path) => update('vaultContainer', path))}
        >
          {t('picker.browse_file')}
        </Button>
      </div>
      <TextField
        label={t('folder_share.vault_pim')}
        value={form.vaultPIM}
        onValueChange={(value) => update('vaultPIM', value)}
        type="number"
        min={0}
        max={MAX_VAULT_PIM}
      />
      <p className={adminStyles.hint}>{t('folder_share.vault_pim_hint')}</p>
      <TextField
        label={t('folder_share.vault_password')}
        value={form.vaultPassword}
        onValueChange={(value) => update('vaultPassword', value)}
        type="password"
        autoComplete="off"
      />
      {creating ? (
        <>
          <Switch
            checked={form.vaultCreate}
            label={t('folder_share.vault_create')}
            onChange={(checked) => update('vaultCreate', checked)}
          />
          {form.vaultCreate ? (
            <TextField
              label={t('folder_share.vault_size')}
              value={form.vaultSizeMiB}
              onValueChange={(value) => update('vaultSizeMiB', value)}
              type="number"
              min={MIN_VAULT_SIZE}
              max={MAX_VAULT_SIZE}
            />
          ) : null}
        </>
      ) : (
        <p className={adminStyles.hint}>{t('folder_share.keep_stored_credential')}</p>
      )}
      <p className={adminStyles.hint}>{t('folder_share.vault_hint')}</p>
    </>
  )
}

interface LocalPathFieldProps {
  value: string
  onChange: (value: string) => void
  openPathPicker: (start: string, apply: (path: string) => void) => void
  placeholder?: string
}

function LocalPathField({ value, onChange, openPathPicker, placeholder }: LocalPathFieldProps) {
  const { t } = useI18n()
  return (
    <div className={styles.pathRow}>
      <TextField
        className={styles.pathRowField}
        label={t('folder_share.server_path')}
        placeholder={placeholder}
        value={value}
        onValueChange={onChange}
        autoComplete="off"
      />
      <Button
        className={styles.pathRowButton}
        variant="outlined"
        icon={<Icon name="folder" />}
        onClick={() => openPathPicker(value, onChange)}
      >
        {t('picker.browse_folder')}
      </Button>
    </div>
  )
}

export function ShareManagementSection() {
  const { t } = useI18n()
  const sharesQuery = useAdminShares()
  const shares = sharesQuery.data ?? []
  const encryptionQuery = useShareEncryptions()
  const encryptedByShare = useMemo(
    () => new Map((encryptionQuery.data ?? []).map((entry) => [entry.share, entry])),
    [encryptionQuery.data]
  )

  const addMutation = useCreateShare()
  const editMutation = useUpdateShare()
  const deleteMutation = useDeleteShare()
  const trashMutation = useUpdateShare()
  const retryMutation = useRetryShare()
  const enableEncryptionMutation = useEnableShareEncryption()
  const disableEncryptionMutation = useDisableShareEncryption()

  const [state, patchState] = useShareManagementState()
  const {
    addOpen,
    addName,
    addBackend,
    addForm,
    addValidation,
    editTarget,
    editName,
    editForm,
    editValidation,
    deleteTarget,
    encEnableTarget,
    encPassphrase,
    encPassphraseConfirm,
    encGenerating,
    encGenerateError,
    encDisableTarget,
    encDisableError,
    announcement
  } = state
  const setAddOpen = (value: boolean): void => patchState({ addOpen: value })
  const setAddName = (value: string): void => patchState({ addName: value })
  const setAddBackend = (value: ShareBackend): void => patchState({ addBackend: value })
  const setAddForm: Dispatch<SetStateAction<BackendForm>> = (value) =>
    patchState((current) => ({ addForm: typeof value === 'function' ? value(current.addForm) : value }))
  const setAddValidation = (value: string | null): void => patchState({ addValidation: value })
  const setEditTarget = (value: AdminShare | null): void => patchState({ editTarget: value })
  const setEditName = (value: string): void => patchState({ editName: value })
  const setEditForm: Dispatch<SetStateAction<BackendForm>> = (value) =>
    patchState((current) => ({ editForm: typeof value === 'function' ? value(current.editForm) : value }))
  const setEditValidation = (value: string | null): void => patchState({ editValidation: value })
  const setDeleteTarget = (value: AdminShare | null): void => patchState({ deleteTarget: value })
  const setEncEnableTarget = (value: AdminShare | null): void => patchState({ encEnableTarget: value })
  const setEncPassphrase = (value: string): void => patchState({ encPassphrase: value })
  const setEncPassphraseConfirm = (value: string): void => patchState({ encPassphraseConfirm: value })
  const setEncGenerating = (value: boolean): void => patchState({ encGenerating: value })
  const setEncGenerateError = (value: string | null): void => patchState({ encGenerateError: value })
  const setEncDisableTarget = (value: AdminShare | null): void => patchState({ encDisableTarget: value })
  const setEncDisableError = (value: string | null): void => patchState({ encDisableError: value })
  const setAnnouncement = (value: string): void => patchState({ announcement: value })

  const addError =
    addValidation ?? (addMutation.error ? errorText(addMutation.error, t('common.could_not_add_folder'), t) : null)
  const editError =
    editValidation ?? (editMutation.error ? errorText(editMutation.error, t('common.could_not_save_change'), t) : null)
  const deleteError = deleteMutation.error ? errorText(deleteMutation.error, t('common.could_not_remove'), t) : null
  const trashError = trashMutation.error
    ? errorText(trashMutation.error, t('folder_share.could_not_change_trash_setting'), t)
    : null
  const retryError = retryMutation.error
    ? errorText(retryMutation.error, t('folder_share.the_folder_is_still_unavailable'), t)
    : null
  const encryptionLoadError = encryptionQuery.error
    ? describeApiError(encryptionQuery.error, t('encryption.could_not_load_status'))
    : null
  const encryptionEnableError = encGenerateError
  const trashTogglingId = trashMutation.isPending ? (trashMutation.variables?.id ?? null) : null
  const retryingId = retryMutation.isPending ? (retryMutation.variables ?? null) : null
  const passphraseMismatch = encPassphraseConfirm.length > 0 && encPassphrase !== encPassphraseConfirm

  const openPathPicker = async (mode: 'folder' | 'file', start: string, apply: (path: string) => void) => {
    const picked = await pickPath({ mode, start })
    if (picked !== null) apply(picked)
  }

  const openAdd = () => {
    addMutation.reset()
    setAddValidation(null)
    setAddName('')
    setAddBackend('local')
    setAddForm(emptyBackendForm())
    setAddOpen(true)
  }
  const closeAdd = () => {
    if (!addMutation.isPending) setAddOpen(false)
  }

  const submitAdd = async () => {
    setAddValidation(null)
    if (!addName.trim()) {
      setAddValidation(t('folder_share.enter_name'))
      return
    }
    const validation = validateBackend(t, addBackend, addForm)
    if (validation) {
      setAddValidation(validation)
      return
    }
    const request: CreateShareReq = { name: addName.trim(), backend: addBackend }
    if (addBackend === 'local') request.host = addForm.hostPath.trim()
    else if (addBackend === 's3') request.s3 = s3Of(addForm)
    else request.veracrypt = vaultOf(addForm)
    try {
      await addMutation.mutateAsync(request)
      setAddOpen(false)
    } catch {
      return
    }
  }

  const openEdit = (share: AdminShare) => {
    editMutation.reset()
    setEditValidation(null)
    setEditTarget(share)
    setEditName(share.name)
    setEditForm({ ...emptyBackendForm(), hostPath: share.host, s3Region: '' })
  }
  const closeEdit = () => {
    if (!editMutation.isPending) setEditTarget(null)
  }

  const submitEdit = async () => {
    if (!editTarget) return
    setEditValidation(null)
    if (!editName.trim()) {
      setEditValidation(t('folder_share.enter_name'))
      return
    }
    if (editTarget.backend === 'local' && !editForm.hostPath.trim()) {
      setEditValidation(t('folder_share.enter_server_path'))
      return
    }
    const patch: UpdateShareReq = { name: editName.trim() }
    if (editTarget.backend === 'local' && editForm.hostPath.trim() !== editTarget.host)
      patch.host = editForm.hostPath.trim()
    if (editTarget.backend === 's3') {
      const config = s3PatchOf(editForm)
      if (config) patch.s3 = config
    } else if (editTarget.backend === 'veracrypt') {
      const config = vaultPatchOf(editForm)
      if (config) patch.veracrypt = config
    }
    try {
      await editMutation.mutateAsync({ id: editTarget.id, patch })
      setEditTarget(null)
    } catch {
      return
    }
  }

  const toggleTrash = async (share: AdminShare, enabled: boolean) => {
    trashMutation.reset()
    try {
      await trashMutation.mutateAsync({ id: share.id, patch: { trash_enabled: enabled } })
    } catch {
      return
    }
  }

  const retry = async (share: AdminShare) => {
    retryMutation.reset()
    try {
      await retryMutation.mutateAsync(share.id)
    } catch {
      return
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    try {
      await deleteMutation.mutateAsync(deleteTarget.id)
      setDeleteTarget(null)
    } catch {
      return
    }
  }

  const openDelete = (share: AdminShare) => {
    deleteMutation.reset()
    setDeleteTarget(share)
  }

  const openEncryptionEnable = (share: AdminShare) => {
    setEncEnableTarget(share)
    setEncPassphrase('')
    setEncPassphraseConfirm('')
    setEncGenerateError(null)
  }
  const closeEncryptionEnable = () => {
    if (encGenerating) return
    setEncEnableTarget(null)
    setEncPassphrase('')
    setEncPassphraseConfirm('')
    setEncGenerateError(null)
  }

  const enableEncryption = async () => {
    const target = encEnableTarget
    if (!target || !encPassphrase || passphraseMismatch) return
    setEncGenerateError(null)
    setEncGenerating(true)
    let keys: DerivedKeys | null = null
    try {
      const salt = generateSalt()
      keys = await deriveKeys(encPassphrase, salt)
      const verifier = await makeVerifier(keys)
      await enableEncryptionMutation.mutateAsync({ id: target.id, scheme: 'rclone-crypt-v1', salt, verifier })
      await unlock(encPassphrase, salt, verifier)
      setAnnouncement(t('encryption.enabled_for', { name: target.name }))
      setEncEnableTarget(null)
      setEncPassphrase('')
      setEncPassphraseConfirm('')
    } catch (error) {
      setEncGenerateError(
        error instanceof ApiError
          ? describeApiError(error, t('encryption.could_not_enable'))
          : t('encryption.could_not_create_key')
      )
    } finally {
      setEncGenerating(false)
      if (keys) clean(keys.dataKey)
    }
  }

  const openEncryptionDisable = (share: AdminShare) => {
    setEncDisableTarget(share)
    setEncDisableError(null)
  }
  const closeEncryptionDisable = () => {
    if (!disableEncryptionMutation.isPending) setEncDisableTarget(null)
  }
  const disableEncryption = async () => {
    const target = encDisableTarget
    if (!target) return
    setEncDisableError(null)
    try {
      await disableEncryptionMutation.mutateAsync(target.id)
      setAnnouncement(t('encryption.disabled_for', { name: target.name }))
      setEncDisableTarget(null)
    } catch (error) {
      setEncDisableError(describeApiError(error, t('encryption.could_not_disable')))
    }
  }

  const copySalt = async (salt: string, name: string) => {
    if (!salt || typeof navigator === 'undefined' || !navigator.clipboard) return
    try {
      await navigator.clipboard.writeText(salt)
      setAnnouncement(t('encryption.salt_copied', { name }))
    } catch {
      return
    }
  }

  const backendOptions = [
    { value: 'local', text: t('folder_share.backend_local') },
    { value: 's3', text: t('folder_share.backend_s3') },
    { value: 'veracrypt', text: t('folder_share.backend_veracrypt') }
  ]
  const renderBackendFields = (
    backend: ShareBackend,
    form: BackendForm,
    setForm: Dispatch<SetStateAction<BackendForm>>,
    creating: boolean
  ) =>
    backend === 's3' ? (
      <S3Fields form={form} setForm={setForm} creating={creating} />
    ) : (
      <VaultFields
        form={form}
        setForm={setForm}
        creating={creating}
        openPathPicker={(start, apply) => void openPathPicker('file', start, apply)}
      />
    )

  const renderLocalFields = (form: BackendForm, setForm: Dispatch<SetStateAction<BackendForm>>, creating: boolean) => (
    <>
      <LocalPathField
        value={form.hostPath}
        onChange={(value) => setForm((current) => ({ ...current, hostPath: value }))}
        openPathPicker={(start, apply) => void openPathPicker('folder', start, apply)}
        placeholder={creating ? t('folder_share.e_g_srv_photos') : undefined}
      />
      {creating ? <p className={adminStyles.hint}>{t('folder_share.enter_path_folder_already_exists')}</p> : null}
    </>
  )
  return (
    <>
      <section className={adminStyles.section}>
        <h2 className={adminStyles.sectionTitle}>{t('folder_share.folder_shares')}</h2>
        <p className={adminStyles.hint}>{t('folder_share.registers_real_folder_on_server')}</p>
        {sharesQuery.isPending ? (
          <ProgressCircular />
        ) : sharesQuery.error ? (
          <p className={adminStyles.error} role="alert">
            {describeApiError(sharesQuery.error, t('folder_share.could_not_load_share_list'))}
          </p>
        ) : (
          <>
            <ShareManagementList
              t={t}
              shares={shares}
              encryptionByShare={encryptedByShare}
              encryptionLoaded={Boolean(encryptionQuery.data)}
              pinnedKeys={[
                editTarget?.id,
                deleteTarget?.id,
                encEnableTarget?.id,
                encDisableTarget?.id,
                trashTogglingId,
                retryingId
              ].filter((id): id is number => id != null)}
              trashTogglingId={trashTogglingId}
              retryingId={retryingId}
              trashError={trashError}
              retryError={retryError}
              onEnableEncryption={openEncryptionEnable}
              onDisableEncryption={openEncryptionDisable}
              onCopySalt={(salt, name) => void copySalt(salt, name)}
              onToggleTrash={(share, enabled) => void toggleTrash(share, enabled)}
              onRetry={(share) => void retry(share)}
              onEdit={openEdit}
              onDelete={openDelete}
            />
            {encryptionLoadError ? (
              <p className={adminStyles.error} role="alert">
                {encryptionLoadError}
              </p>
            ) : null}
            <p className={styles.encAnnounce} aria-live="polite">
              {announcement}
            </p>
            <Button variant="tonal" icon={<Icon name="add" />} onClick={openAdd}>
              {t('common.add_folder')}
            </Button>
          </>
        )}
      </section>

      <Dialog
        open={addOpen}
        title={t('common.add_folder')}
        onClose={closeAdd}
        actions={
          <>
            <Button variant="text" disabled={addMutation.isPending} onClick={closeAdd}>
              {t('common.cancel')}
            </Button>
            <Button loading={addMutation.isPending} onClick={() => void submitAdd()}>
              {t('common.add')}
            </Button>
          </>
        }
      >
        <form
          className={adminStyles.form}
          onSubmit={(event) => {
            event.preventDefault()
            void submitAdd()
          }}
        >
          <TextField
            label={t('common.name')}
            placeholder={t('folder_share.e_g_photos')}
            value={addName}
            onValueChange={setAddName}
            autoComplete="off"
          />
          <Select
            label={t('folder_share.backend')}
            options={backendOptions}
            value={addBackend}
            testid="share-backend-select"
            onValueChange={(value) => setAddBackend(value as ShareBackend)}
          />
          {addBackend === 'local'
            ? renderLocalFields(addForm, setAddForm, true)
            : renderBackendFields(addBackend, addForm, setAddForm, true)}
          {addError ? (
            <p className={adminStyles.error} role="alert">
              {addError}
            </p>
          ) : null}
        </form>
      </Dialog>

      <Dialog
        open={editTarget !== null}
        title={t('folder_share.edit_folder')}
        onClose={closeEdit}
        actions={
          <>
            <Button variant="text" disabled={editMutation.isPending} onClick={closeEdit}>
              {t('common.cancel')}
            </Button>
            <Button loading={editMutation.isPending} onClick={() => void submitEdit()}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        {editTarget ? (
          <form
            className={adminStyles.form}
            onSubmit={(event) => {
              event.preventDefault()
              void submitEdit()
            }}
          >
            <TextField label={t('common.name')} value={editName} onValueChange={setEditName} autoComplete="off" />
            <p className={adminStyles.hint}>
              {t('folder_share.backend_fixed', { backend: backendLabel(t, editTarget.backend) })}
            </p>
            {editTarget.backend === 'local' ? (
              renderLocalFields(editForm, setEditForm, false)
            ) : (
              <>
                <p className={adminStyles.hint} data-testid="edit-share-source">
                  {t('folder_share.current_location', { source: editTarget.source })}
                </p>
                {renderBackendFields(editTarget.backend, editForm, setEditForm, false)}
              </>
            )}
            {editError ? (
              <p className={adminStyles.error} role="alert">
                {editError}
              </p>
            ) : null}
          </form>
        ) : null}
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        title={t('folder_share.remove_share')}
        onClose={() => {
          if (!deleteMutation.isPending) setDeleteTarget(null)
        }}
        actions={
          <>
            <Button variant="text" disabled={deleteMutation.isPending} onClick={() => setDeleteTarget(null)}>
              {t('common.cancel')}
            </Button>
            <Button danger loading={deleteMutation.isPending} onClick={() => void confirmDelete()}>
              {t('common.remove_2')}
            </Button>
          </>
        }
      >
        {deleteTarget ? (
          <>
            <p>{t('folder_share.removes_every_user_permission_granted', { name: deleteTarget.name })}</p>
            {deleteError ? (
              <p className={adminStyles.error} role="alert">
                {deleteError}
              </p>
            ) : null}
          </>
        ) : null}
      </Dialog>

      <Dialog
        open={encEnableTarget !== null}
        title={t('encryption.enable_title', { name: encEnableTarget?.name ?? '' })}
        onClose={closeEncryptionEnable}
        actions={
          <>
            <Button variant="text" disabled={encGenerating} onClick={closeEncryptionEnable}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={encGenerating}
              disabled={!encPassphrase || !encPassphraseConfirm || passphraseMismatch}
              onClick={() => void enableEncryption()}
            >
              {t('encryption.enable')}
            </Button>
          </>
        }
      >
        {encEnableTarget ? (
          <div className={adminStyles.form}>
            <p>{t('encryption.enable_hint')}</p>
            <p className={adminStyles.warning}>{t('encryption.passphrase_warning')}</p>
            <p className={adminStyles.hint}>{t('encryption.verifier_note')}</p>
            <form
              onSubmit={(event) => {
                event.preventDefault()
                void enableEncryption()
              }}
            >
              <TextField
                type="password"
                label={t('encryption.passphrase')}
                value={encPassphrase}
                onValueChange={setEncPassphrase}
                autoComplete="new-password"
              />
              <TextField
                type="password"
                label={t('encryption.confirm_passphrase')}
                value={encPassphraseConfirm}
                onValueChange={setEncPassphraseConfirm}
                error={passphraseMismatch ? t('encryption.passphrases_do_not_match') : null}
                autoComplete="new-password"
              />
              {encryptionEnableError ? (
                <p className={adminStyles.error} role="alert">
                  {encryptionEnableError}
                </p>
              ) : null}
            </form>
          </div>
        ) : null}
      </Dialog>

      <Dialog
        open={encDisableTarget !== null}
        title={t('encryption.disable_title', { name: encDisableTarget?.name ?? '' })}
        onClose={closeEncryptionDisable}
        actions={
          <>
            <Button variant="text" disabled={disableEncryptionMutation.isPending} onClick={closeEncryptionDisable}>
              {t('common.cancel')}
            </Button>
            <Button loading={disableEncryptionMutation.isPending} onClick={() => void disableEncryption()}>
              {t('encryption.disable')}
            </Button>
          </>
        }
      >
        {encDisableTarget ? (
          <>
            <p>{t('encryption.disable_hint')}</p>
            {encDisableError ? (
              <p className={adminStyles.error} role="alert">
                {encDisableError}
              </p>
            ) : null}
          </>
        ) : null}
      </Dialog>
    </>
  )
}
