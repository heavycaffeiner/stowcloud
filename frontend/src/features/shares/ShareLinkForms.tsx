import { Controller, useForm, useWatch, type Control } from 'react-hook-form'
import { describeApiError } from '../../api/error-text'
import { t } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { Button } from '../../ui/Button'
import { FormTextField } from '../../ui/FormTextField'
import { Select, type SelectOption } from '../../ui/Select'
import { Switch } from '../../ui/Switch'
import { useCreateShareLink, useUpdateShareLink, type ShareLinkInfo } from '../links/api'
import * as styles from './ShareManageDialog.css'

const PRESET_DAYS: Record<string, number> = { '1d': 1, '7d': 7, '30d': 30 }

function isoToExpiresNs(iso: string): string | undefined {
  const [year, month, day] = iso.split('-').map(Number)
  if (!year || !month || !day) return undefined
  return String(BigInt(new Date(year, month - 1, day, 23, 59, 59, 999).getTime()) * 1_000_000n)
}

function expiryToNs(choice: string, iso: string): string | undefined {
  if (choice === 'custom') return isoToExpiresNs(iso)
  if (choice === 'none' || choice === 'keep') return undefined
  return String(BigInt(Date.now() + PRESET_DAYS[choice] * 86_400_000) * 1_000_000n)
}

function expiryUnusable(choice: string, iso: string): boolean {
  if (choice !== 'custom') return false
  const ns = isoToExpiresNs(iso)
  return ns === undefined || BigInt(ns) <= BigInt(Date.now()) * 1_000_000n
}

const newExpiryOptions = (): SelectOption[] => [
  { value: 'none', text: t('share.none') },
  { value: '1d', text: t('share.1_day') },
  { value: '7d', text: t('share.7_days') },
  { value: '30d', text: t('share.30_days_default') },
  { value: 'custom', text: t('share.pick_a_date') }
]

const editExpiryOptions = (): SelectOption[] => [
  { value: 'keep', text: t('share.leave_unchanged') },
  { value: 'none', text: t('share.none') },
  { value: '1d', text: t('share.1_day') },
  { value: '7d', text: t('share.7_days') },
  { value: '30d', text: t('share.30_days') },
  { value: 'custom', text: t('share.pick_a_date') }
]

const downloadLimit = (value: string): string | true => {
  const text = value.trim()
  return !text || (Number.isInteger(Number(text)) && Number(text) > 0) || t('share.download_limit_must_integer_1')
}

const limitOf = (value: string): number | null => (value.trim() ? Number(value.trim()) : null)

interface LinkValues {
  kind: 'download' | 'drop'
  read: boolean
  download: boolean
  password: string
  clearPassword: boolean
  expiry: string
  expiryDate: string
  maxDownloads: string
  label: string
}

function PermSwitches({ control }: { control: Control<LinkValues> }) {
  const { t } = useI18n()
  return (
    <div className={styles.permRow}>
      <Controller
        control={control}
        name="read"
        render={({ field }) => <Switch checked={field.value} label={t('share.read_view')} onChange={field.onChange} />}
      />
      <Controller
        control={control}
        name="download"
        render={({ field }) => <Switch checked={field.value} label={t('common.download')} onChange={field.onChange} />}
      />
    </div>
  )
}

function ExpiryFields({ control, options }: { control: Control<LinkValues>; options: SelectOption[] }) {
  const { t } = useI18n()
  const expiry = useWatch({ control, name: 'expiry' })
  return (
    <>
      <Controller
        control={control}
        name="expiry"
        render={({ field }) => (
          <Select label={t('share.expiry')} options={options} value={field.value} onValueChange={field.onChange} />
        )}
      />
      {expiry === 'custom' ? (
        <FormTextField
          control={control}
          name="expiryDate"
          rules={{
            validate: (value, values) =>
              !expiryUnusable(values.expiry, value) || t('share.expiry_date_must_be_in_the_future')
          }}
          type="date"
          label={t('share.expiry_date')}
        />
      ) : null}
    </>
  )
}

interface NewLinkFormProps {
  path: string
  targetName: string
  targetIsDir: boolean
  onCreated: (link: ShareLinkInfo) => void
  onCancel: () => void
}

export function NewLinkForm({ path, targetName, targetIsDir, onCreated, onCancel }: NewLinkFormProps) {
  const { t } = useI18n()
  const create = useCreateShareLink()
  const { control, handleSubmit } = useForm<LinkValues>({
    defaultValues: {
      kind: 'download',
      read: true,
      download: true,
      password: '',
      clearPassword: false,
      expiry: '30d',
      expiryDate: '',
      maxDownloads: '',
      label: ''
    }
  })
  const [kind, read, download] = useWatch({ control, name: ['kind', 'read', 'download'] })
  const submit = handleSubmit((values) => {
    if (create.isPending) return
    const isDrop = values.kind === 'drop'
    create.mutate(
      {
        path,
        perms: isDrop ? { create: true } : { read: values.read, download: values.download },
        password: values.password.trim() || undefined,
        expires_ns: expiryToNs(values.expiry, values.expiryDate),
        max_downloads: isDrop ? undefined : (limitOf(values.maxDownloads) ?? undefined),
        label: values.label.trim() || undefined
      },
      { onSuccess: onCreated }
    )
  })
  const kindOptions: SelectOption[] = [
    { value: 'download', text: t('share.kind_download') },
    ...(targetIsDir ? [{ value: 'drop', text: t('share.kind_drop') }] : [])
  ]

  return (
    <form className={styles.createForm} onSubmit={(event) => void submit(event)}>
      <h3 className={styles.createTitle}>{t('share.create_new_link')}</h3>
      <Controller
        control={control}
        name="kind"
        render={({ field }) => (
          <Select
            label={t('share.kind_label')}
            options={kindOptions}
            value={field.value}
            onValueChange={field.onChange}
          />
        )}
      />
      {kind === 'drop' ? <p className={styles.hint}>{t('share.drop_hint')}</p> : <PermSwitches control={control} />}
      <ExpiryFields control={control} options={newExpiryOptions()} />
      <FormTextField control={control} name="password" type="password" label={t('share.password_optional')} />
      {kind !== 'drop' ? (
        <FormTextField
          control={control}
          name="maxDownloads"
          rules={{ validate: downloadLimit }}
          label={t('share.download_limit_optional')}
          placeholder={t('share.no_limit')}
        />
      ) : null}
      <FormTextField control={control} name="label" label={t('share.label_optional')} placeholder={targetName} />
      {create.error ? (
        <p className={styles.error} role="alert">
          {describeApiError(create.error, t('share.could_not_create_share_link'))}
        </p>
      ) : null}
      <div className={styles.editActions}>
        <Button variant="text" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={create.isPending || (kind !== 'drop' && !read && !download)}>
          {t('common.create')}
        </Button>
      </div>
    </form>
  )
}

interface EditLinkFormProps {
  link: ShareLinkInfo
  onDone: () => void
}

export function EditLinkForm({ link, onDone }: EditLinkFormProps) {
  const { t } = useI18n()
  const update = useUpdateShareLink()
  const { control, handleSubmit } = useForm<LinkValues>({
    defaultValues: {
      kind: 'download',
      read: link.perms.read,
      download: link.perms.download,
      password: '',
      clearPassword: false,
      expiry: 'keep',
      expiryDate: '',
      maxDownloads: link.max_downloads === null ? '' : String(link.max_downloads),
      label: link.label ?? ''
    }
  })
  const clearPassword = useWatch({ control, name: 'clearPassword' })
  const submit = handleSubmit((values) => {
    if (update.isPending) return
    update.mutate(
      {
        id: link.id,
        patch: {
          perms: { read: values.read, download: values.download, create: link.perms.create },
          password: values.clearPassword ? null : values.password.trim() || undefined,
          expires_ns: values.expiry === 'keep' ? undefined : (expiryToNs(values.expiry, values.expiryDate) ?? null),
          max_downloads: limitOf(values.maxDownloads),
          label: values.label.trim()
        }
      },
      { onSuccess: onDone }
    )
  })

  return (
    <form className={styles.editForm} onSubmit={(event) => void submit(event)}>
      <PermSwitches control={control} />
      <ExpiryFields control={control} options={editExpiryOptions()} />
      <FormTextField
        control={control}
        name="maxDownloads"
        rules={{ validate: downloadLimit }}
        label={t('share.download_limit_optional')}
        placeholder={t('share.no_limit')}
      />
      <FormTextField control={control} name="label" label={t('share.label_optional')} />
      {link.has_password && !clearPassword ? (
        <Controller
          control={control}
          name="clearPassword"
          render={({ field }) => (
            <Switch checked={field.value} label={t('share.remove_password')} onChange={field.onChange} />
          )}
        />
      ) : null}
      {!clearPassword ? (
        <FormTextField control={control} name="password" type="password" label={t('share.new_password_optional')} />
      ) : null}
      {update.error ? (
        <p className={styles.error} role="alert">
          {describeApiError(update.error, t('share.could_not_update_share_link'))}
        </p>
      ) : null}
      <div className={styles.editActions}>
        <Button variant="text" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={update.isPending}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  )
}
