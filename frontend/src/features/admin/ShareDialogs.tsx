import { useMutation } from '@tanstack/react-query'
import { overlay } from 'overlay-kit'
import { Controller, FormProvider, useForm, useFormContext, useWatch } from 'react-hook-form'
import { clean } from '@noble/ciphers/utils.js'
import { describeApiError } from '../../api/error-text'
import { ApiError } from '../../api/fetcher'
import { deriveKeys, generateSalt, makeVerifier, unlock } from '../../lib/crypto/e2ee'
import { t } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { FormTextField } from '../../ui/FormTextField'
import { Icon } from '../../ui/Icon'
import { Select } from '../../ui/Select'
import { Switch } from '../../ui/Switch'
import { pickPath } from '../files/PathPickerDialog'
import { backendLabel } from './ShareManagementList'
import * as styles from './ShareManagementSection.css'
import * as adminStyles from './admin.css'
import {
  useCreateShare,
  useEnableShareEncryption,
  useUpdateShare,
  type AdminShare,
  type CreateShareReq,
  type ShareBackend,
  type ShareS3Config,
  type ShareVeracryptConfig,
  type UpdateShareReq
} from './api'

const MIN_VAULT_SIZE = 16
const MAX_VAULT_SIZE = 1 << 20
const MAX_VAULT_PIM = 10000

interface ShareValues {
  name: string
  backend: ShareBackend
  hostPath: string
  s3Endpoint: string
  s3Region: string
  s3Bucket: string
  s3Prefix: string
  s3AccessKey: string
  s3SecretKey: string
  s3PathStyle: boolean
  // The stored setting is not readable, so an edit sends it only once the switch was used.
  s3PathStyleTouched: boolean
  vaultContainer: string
  vaultPassword: string
  vaultCreate: boolean
  vaultSizeMiB: string
  vaultPIM: string
}

function initialValues(share?: AdminShare): ShareValues {
  return {
    name: share?.name ?? '',
    backend: share?.backend ?? 'local',
    hostPath: share?.host ?? '',
    s3Endpoint: '',
    s3Region: share ? '' : 'us-east-1',
    s3Bucket: '',
    s3Prefix: '',
    s3AccessKey: '',
    s3SecretKey: '',
    s3PathStyle: true,
    s3PathStyleTouched: false,
    vaultContainer: '',
    vaultPassword: '',
    vaultCreate: true,
    vaultSizeMiB: '256',
    vaultPIM: ''
  }
}

export function shareErrorText(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code === 'fs.not_found') return t('common.share_no_longer_exists')
  return describeApiError(error, fallback)
}

function backendProblem(values: ShareValues): string | null {
  if (values.backend === 'local') return values.hostPath.trim() ? null : t('folder_share.enter_server_path')
  if (values.backend === 's3') {
    if (!values.s3Endpoint.trim()) return t('folder_share.enter_endpoint')
    if (!values.s3Region.trim()) return t('folder_share.enter_region')
    if (!values.s3Bucket.trim()) return t('folder_share.enter_bucket')
    if (!values.s3AccessKey.trim()) return t('folder_share.enter_access_key')
    if (!values.s3SecretKey) return t('folder_share.enter_secret_key')
    return null
  }
  if (!values.vaultContainer.trim()) return t('folder_share.enter_container_path')
  if (!values.vaultPassword) return t('folder_share.enter_password')
  if (values.vaultCreate && !(Number(values.vaultSizeMiB) >= MIN_VAULT_SIZE))
    return t('folder_share.size_at_least', { min: String(MIN_VAULT_SIZE) })
  if (values.vaultCreate && Number(values.vaultSizeMiB) > MAX_VAULT_SIZE)
    return t('folder_share.size_at_most', { max: String(MAX_VAULT_SIZE) })
  if (values.vaultPIM !== '' && !(Number(values.vaultPIM) >= 0 && Number(values.vaultPIM) <= MAX_VAULT_PIM))
    return t('folder_share.pim_at_most', { max: String(MAX_VAULT_PIM) })
  return null
}

// An edit leaves blank credentials as stored, so only the name and a local path are required.
function shareProblem(values: ShareValues, share?: AdminShare): string | null {
  if (!values.name.trim()) return t('folder_share.enter_name')
  if (!share) return backendProblem(values)
  return share.backend === 'local' && !values.hostPath.trim() ? t('folder_share.enter_server_path') : null
}

function createRequestOf(values: ShareValues): CreateShareReq {
  const request: CreateShareReq = { name: values.name.trim(), backend: values.backend }
  if (values.backend === 'local') request.host = values.hostPath.trim()
  else if (values.backend === 's3')
    request.s3 = {
      endpoint: values.s3Endpoint.trim(),
      region: values.s3Region.trim(),
      bucket: values.s3Bucket.trim(),
      prefix: values.s3Prefix.trim(),
      access_key_id: values.s3AccessKey.trim(),
      secret_access_key: values.s3SecretKey,
      path_style: values.s3PathStyle
    }
  else
    request.veracrypt = {
      container: values.vaultContainer.trim(),
      password: values.vaultPassword,
      create: values.vaultCreate,
      ...(values.vaultCreate ? { size_mib: Number(values.vaultSizeMiB) } : {}),
      ...(values.vaultPIM !== '' ? { pim: Number(values.vaultPIM) } : {})
    }
  return request
}

/** Drops blank entries, so a patch only carries what the admin typed. */
function filled<T extends object>(config: T): T | undefined {
  const entries = Object.entries(config).filter(([, value]) => value !== '' && value !== undefined)
  return entries.length ? (Object.fromEntries(entries) as T) : undefined
}

function updatePatchOf(share: AdminShare, values: ShareValues): UpdateShareReq {
  const patch: UpdateShareReq = { name: values.name.trim() }
  if (share.backend === 'local' && values.hostPath.trim() !== share.host) patch.host = values.hostPath.trim()
  if (share.backend === 's3') {
    const s3 = filled<ShareS3Config>({
      endpoint: values.s3Endpoint.trim(),
      region: values.s3Region.trim(),
      bucket: values.s3Bucket.trim(),
      prefix: values.s3Prefix.trim(),
      access_key_id: values.s3AccessKey.trim(),
      secret_access_key: values.s3SecretKey,
      path_style: values.s3PathStyleTouched ? values.s3PathStyle : undefined
    })
    if (s3) patch.s3 = s3
  }
  if (share.backend === 'veracrypt') {
    const veracrypt = filled<ShareVeracryptConfig>({
      container: values.vaultContainer.trim(),
      password: values.vaultPassword,
      pim: values.vaultPIM !== '' ? Number(values.vaultPIM) : undefined
    })
    if (veracrypt) patch.veracrypt = veracrypt
  }
  return patch
}

interface DialogControls<T> {
  open: boolean
  onDone: (result: T) => void
  onClosed: () => void
}

/** Adds a share, or edits the given one. Resolves to the saved share, or null when cancelled. */
export function askShare(share?: AdminShare): Promise<AdminShare | null> {
  return overlay.openAsync<AdminShare | null>(({ isOpen, close, unmount }) => (
    <ShareDialog share={share} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

function ShareDialog({ share, open, onDone, onClosed }: DialogControls<AdminShare | null> & { share?: AdminShare }) {
  const { t } = useI18n()
  const create = useCreateShare()
  const update = useUpdateShare()
  const save = share ? update : create
  const form = useForm<ShareValues>({ defaultValues: initialValues(share) })
  const { control, handleSubmit, setError, formState } = form
  const backend = useWatch({ control, name: 'backend' })
  const creating = !share
  const submit = handleSubmit((values) => {
    if (save.isPending) return
    const problem = shareProblem(values, share)
    if (problem) setError('root', { message: problem })
    else if (share) update.mutate({ id: share.id, patch: updatePatchOf(share, values) }, { onSuccess: onDone })
    else create.mutate(createRequestOf(values), { onSuccess: onDone })
  })
  const cancel = (): void => {
    if (!save.isPending) onDone(null)
  }
  const error =
    formState.errors.root?.message ??
    (save.error
      ? shareErrorText(save.error, share ? t('common.could_not_save_change') : t('common.could_not_add_folder'))
      : null)

  return (
    <Dialog
      open={open}
      title={share ? t('folder_share.edit_folder') : t('common.add_folder')}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" disabled={save.isPending} onClick={cancel}>
            {t('common.cancel')}
          </Button>
          <Button loading={save.isPending} onClick={() => void submit()}>
            {share ? t('common.save') : t('common.add')}
          </Button>
        </>
      }
    >
      <FormProvider {...form}>
        <form className={adminStyles.form} onSubmit={(event) => void submit(event)}>
          <FormTextField
            control={control}
            name="name"
            label={t('common.name')}
            placeholder={creating ? t('folder_share.e_g_photos') : undefined}
            autoComplete="off"
          />
          {share ? (
            <p className={adminStyles.hint}>
              {t('folder_share.backend_fixed', { backend: backendLabel(share.backend) })}
            </p>
          ) : (
            <Controller
              control={control}
              name="backend"
              render={({ field }) => (
                <Select
                  label={t('folder_share.backend')}
                  options={[
                    { value: 'local', text: t('folder_share.backend_local') },
                    { value: 's3', text: t('folder_share.backend_s3') },
                    { value: 'veracrypt', text: t('folder_share.backend_veracrypt') }
                  ]}
                  value={field.value}
                  testid="share-backend-select"
                  onValueChange={(value) => field.onChange(value as ShareBackend)}
                />
              )}
            />
          )}
          {share && share.backend !== 'local' ? (
            <p className={adminStyles.hint} data-testid="edit-share-source">
              {t('folder_share.current_location', { source: share.source })}
            </p>
          ) : null}
          {backend === 'local' ? (
            <>
              <PathField
                name="hostPath"
                mode="folder"
                label={t('folder_share.server_path')}
                placeholder={creating ? t('folder_share.e_g_srv_photos') : undefined}
              />
              {creating ? (
                <p className={adminStyles.hint}>{t('folder_share.enter_path_folder_already_exists')}</p>
              ) : null}
            </>
          ) : backend === 's3' ? (
            <S3Fields creating={creating} />
          ) : (
            <VaultFields creating={creating} />
          )}
          {error ? (
            <p className={adminStyles.error} role="alert">
              {error}
            </p>
          ) : null}
        </form>
      </FormProvider>
    </Dialog>
  )
}

interface PathFieldProps {
  name: 'hostPath' | 'vaultContainer'
  mode: 'folder' | 'file'
  label: string
  placeholder?: string
}

function PathField({ name, mode, label, placeholder }: PathFieldProps) {
  const { t } = useI18n()
  const { control, getValues, setValue } = useFormContext<ShareValues>()
  async function browse(): Promise<void> {
    const picked = await pickPath({ mode, start: getValues(name) })
    if (picked !== null) setValue(name, picked, { shouldDirty: true })
  }
  return (
    <div className={styles.pathRow}>
      <FormTextField
        control={control}
        name={name}
        className={styles.pathRowField}
        label={label}
        placeholder={placeholder}
        autoComplete="off"
      />
      <Button
        className={styles.pathRowButton}
        variant="outlined"
        icon={<Icon name={mode} />}
        onClick={() => void browse()}
      >
        {mode === 'folder' ? t('picker.browse_folder') : t('picker.browse_file')}
      </Button>
    </div>
  )
}

function S3Fields({ creating }: { creating: boolean }) {
  const { t } = useI18n()
  const { control, setValue } = useFormContext<ShareValues>()
  return (
    <>
      <FormTextField
        control={control}
        name="s3Endpoint"
        label={t('folder_share.s3_endpoint')}
        placeholder={t('folder_share.e_g_s3_endpoint')}
        autoComplete="off"
      />
      <p className={adminStyles.hint}>{t('folder_share.s3_endpoint_scheme_hint')}</p>
      <FormTextField
        control={control}
        name="s3Bucket"
        label={t('folder_share.s3_bucket')}
        placeholder={t('folder_share.e_g_s3_bucket')}
        autoComplete="off"
      />
      <FormTextField control={control} name="s3Region" label={t('folder_share.s3_region')} autoComplete="off" />
      <FormTextField
        control={control}
        name="s3Prefix"
        label={t('folder_share.s3_prefix')}
        placeholder={t('folder_share.e_g_s3_prefix')}
        autoComplete="off"
      />
      <FormTextField control={control} name="s3AccessKey" label={t('folder_share.s3_access_key')} autoComplete="off" />
      <FormTextField
        control={control}
        name="s3SecretKey"
        label={t('folder_share.s3_secret_key')}
        type="password"
        autoComplete="off"
      />
      {!creating ? <p className={adminStyles.hint}>{t('folder_share.keep_stored_credential')}</p> : null}
      <Controller
        control={control}
        name="s3PathStyle"
        render={({ field }) => (
          <Switch
            checked={field.value}
            label={t('folder_share.s3_path_style')}
            onChange={(checked) => {
              field.onChange(checked)
              setValue('s3PathStyleTouched', true)
            }}
          />
        )}
      />
      <p className={adminStyles.hint}>{t('folder_share.s3_hint')}</p>
    </>
  )
}

function VaultFields({ creating }: { creating: boolean }) {
  const { t } = useI18n()
  const { control } = useFormContext<ShareValues>()
  const vaultCreate = useWatch({ control, name: 'vaultCreate' })
  return (
    <>
      <PathField
        name="vaultContainer"
        mode="file"
        label={t('folder_share.vault_container')}
        placeholder={t('folder_share.e_g_vault_container')}
      />
      <FormTextField
        control={control}
        name="vaultPIM"
        label={t('folder_share.vault_pim')}
        type="number"
        min={0}
        max={MAX_VAULT_PIM}
      />
      <p className={adminStyles.hint}>{t('folder_share.vault_pim_hint')}</p>
      <FormTextField
        control={control}
        name="vaultPassword"
        label={t('folder_share.vault_password')}
        type="password"
        autoComplete="off"
      />
      {creating ? (
        <>
          <Controller
            control={control}
            name="vaultCreate"
            render={({ field }) => (
              <Switch checked={field.value} label={t('folder_share.vault_create')} onChange={field.onChange} />
            )}
          />
          {vaultCreate ? (
            <FormTextField
              control={control}
              name="vaultSizeMiB"
              label={t('folder_share.vault_size')}
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

/** Turns on encryption for an empty share and unlocks it in this tab. Resolves to whether it was turned on. */
export function askEnableEncryption(share: AdminShare): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <EnableEncryptionDialog share={share} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

function EnableEncryptionDialog({ share, open, onDone, onClosed }: DialogControls<boolean> & { share: AdminShare }) {
  const { t } = useI18n()
  const enableShare = useEnableShareEncryption()
  // The server keeps only the salt and a verifier; the derived key never leaves this tab.
  const enable = useMutation({
    mutationFn: async (passphrase: string) => {
      const salt = generateSalt()
      const keys = await deriveKeys(passphrase, salt)
      try {
        const verifier = await makeVerifier(keys)
        await enableShare.mutateAsync({ id: share.id, scheme: 'rclone-crypt-v1', salt, verifier })
        await unlock(passphrase, salt, verifier)
      } finally {
        clean(keys.dataKey)
      }
    }
  })
  const { control, handleSubmit } = useForm({ defaultValues: { passphrase: '', confirm: '' } })
  const [passphrase, confirm] = useWatch({ control, name: ['passphrase', 'confirm'] })
  const mismatch = confirm.length > 0 && passphrase !== confirm
  const submit = handleSubmit((values) => {
    if (!enable.isPending && values.passphrase && values.passphrase === values.confirm)
      enable.mutate(values.passphrase, { onSuccess: () => onDone(true) })
  })
  const cancel = (): void => {
    if (!enable.isPending) onDone(false)
  }
  return (
    <Dialog
      open={open}
      title={t('encryption.enable_title', { name: share.name })}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" disabled={enable.isPending} onClick={cancel}>
            {t('common.cancel')}
          </Button>
          <Button
            loading={enable.isPending}
            disabled={!passphrase || !confirm || mismatch}
            onClick={() => void submit()}
          >
            {t('encryption.enable')}
          </Button>
        </>
      }
    >
      <form className={adminStyles.form} onSubmit={(event) => void submit(event)}>
        <p>{t('encryption.enable_hint')}</p>
        <p className={adminStyles.warning}>{t('encryption.passphrase_warning')}</p>
        <p className={adminStyles.hint}>{t('encryption.verifier_note')}</p>
        <FormTextField
          control={control}
          name="passphrase"
          type="password"
          label={t('encryption.passphrase')}
          autoComplete="new-password"
        />
        <FormTextField
          control={control}
          name="confirm"
          type="password"
          label={t('encryption.confirm_passphrase')}
          error={mismatch ? t('encryption.passphrases_do_not_match') : null}
          autoComplete="new-password"
        />
        {enable.error ? (
          <p className={adminStyles.error} role="alert">
            {enable.error instanceof ApiError
              ? describeApiError(enable.error, t('encryption.could_not_enable'))
              : t('encryption.could_not_create_key')}
          </p>
        ) : null}
      </form>
    </Dialog>
  )
}
