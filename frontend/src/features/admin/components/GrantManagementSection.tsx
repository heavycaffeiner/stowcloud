import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import {
  confirmAction,
  cx,
  Icon,
  StowBadge,
  StowButton,
  StowDialog,
  StowFormTextField,
  StowIconButton,
  StowListItem,
  StowProgressCircular,
  StowSelect,
  StowSwitch,
  VirtualList
} from '@/shared/ui'
import { GrantPermissionGrid, usePermLabels } from './GrantPermissionGrid'
import * as styles from './GrantManagementSection.css'
import * as adminStyles from './admin.css'
import {
  ALL_GRANT_PERMS,
  useAdminGrants,
  useAdminShares,
  useCreateGrant,
  useDeleteGrant,
  useUpdateGrant,
  type AdminGrant,
  type AdminShare,
  type GrantPermName,
  type GrantPrincipal
} from '../api'
import { ApiError } from '../../../api/fetcher'

interface GrantsTarget {
  title: string
  /** Who these grants belong to: a user id or a group id, never both. */
  principal: GrantPrincipal
  /** Display name used in the hint and delete confirmation copy. */
  label: string
}

/** Shows the folders a user or group can see. Settles once it has closed. */
export function showGrants(target: GrantsTarget): Promise<void> {
  return overlay.openAsync<void>(({ isOpen, close, unmount }) => (
    <GrantsDialog {...target} open={isOpen} onClose={() => close()} onClosed={unmount} />
  ))
}

function GrantsDialog({
  title,
  principal,
  label,
  open,
  onClose,
  onClosed
}: GrantsTarget & { open: boolean; onClose: () => void; onClosed: () => void }) {
  const { t } = useI18n()
  return (
    <StowDialog
      open={open}
      title={title}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <StowButton variant="text" onClick={onClose}>
          {t('common.close')}
        </StowButton>
      }
    >
      <GrantManagementSection principal={principal} label={label} />
    </StowDialog>
  )
}

function shareNameOf(shares: readonly AdminShare[], id: number): string {
  return shares.find((share) => share.id === id)?.name ?? t('grant.share', { id })
}

function GrantManagementSection({ principal, label }: Omit<GrantsTarget, 'title'>) {
  const { t } = useI18n()
  const permLabel = usePermLabels()
  const sharesQuery = useAdminShares()
  const grantsQuery = useAdminGrants(principal.kind === 'user' ? { userId: principal.id } : { groupId: principal.id })
  const deleteGrant = useDeleteGrant()
  const shares = sharesQuery.data ?? []
  const grants = grantsQuery.data ?? []
  const [expandedIds, setExpandedIds] = useState<ReadonlySet<number>>(new Set())
  // Keeps the row that opened a dialog mounted so focus can return to it.
  const [pinned, setPinned] = useState<number | null>(null)
  const loadError =
    sharesQuery.error || grantsQuery.error
      ? describeApiError(sharesQuery.error ?? grantsQuery.error, t('grant.could_not_load_permission_list'))
      : null

  function toggleExpanded(id: number): void {
    setExpandedIds((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  function allowSummary(grant: AdminGrant): string {
    if (grant.allow.length === 0) return t('grant.no_permissions_granted')
    if (grant.allow.length === ALL_GRANT_PERMS.length) return t('grant.full_permissions')
    return grant.allow.map((permission) => permLabel[permission]).join(' - ')
  }
  async function withPinned(grant: AdminGrant, task: () => Promise<unknown>): Promise<void> {
    setPinned(grant.id)
    await task()
    setPinned(null)
  }
  function remove(grant: AdminGrant): Promise<void> {
    return withPinned(grant, () =>
      confirmAction({
        title: t('grant.remove_folder_permission'),
        action: t('common.remove_2'),
        danger: true,
        body: (
          <p>
            {t('grant.access_removed_immediately', { name: grant.label || shareNameOf(shares, grant.share) })}{' '}
            {t('grant.will_not_see_folder_from', { principal: label })}
          </p>
        ),
        run: () => deleteGrant.mutateAsync(grant.id),
        describeError: (error) => describeApiError(error, t('common.could_not_remove'))
      })
    )
  }

  return (
    <section className={adminStyles.section}>
      <p className={adminStyles.sectionHint}>
        <strong>{label}</strong>
        {t('grant.sees_only_folders_granted_here')}
      </p>
      {sharesQuery.isPending || grantsQuery.isPending ? (
        <StowProgressCircular />
      ) : loadError ? (
        <p className={adminStyles.sectionError} role="alert">
          {loadError}
        </p>
      ) : (
        <>
          {grants.length === 0 ? (
            <div className={adminStyles.empty}>
              <Icon name="account_tree" />
              <p className={adminStyles.emptyText}>{t('grant.no_folders_granted_yet_signing')}</p>
            </div>
          ) : (
            <VirtualList
              className={adminStyles.list}
              items={grants}
              itemKey={(grant) => grant.id}
              estimateSize={96}
              itemProps={() => ({ className: adminStyles.item })}
              pinnedKeys={pinned === null ? undefined : [pinned]}
              renderItem={(grant) => {
                const expanded = expandedIds.has(grant.id)
                const overlap = grant.allow.filter((permission) => grant.deny.includes(permission))
                const grantName = grant.label || shareNameOf(shares, grant.share)
                return (
                  <StowListItem
                    headline={
                      <>
                        <span className={adminStyles.rowName}>{grantName}</span>
                        {!grant.inherit ? <StowBadge>{t('grant.path_only')}</StowBadge> : null}
                      </>
                    }
                    supporting={
                      <span className={styles.supporting}>
                        <span>
                          {shareNameOf(shares, grant.share)}
                          {grant.subpath ? ` / ${grant.subpath}` : t('grant.root')}
                        </span>
                        <span>
                          {allowSummary(grant)}
                          {grant.deny.length > 0 ? (
                            <span className={styles.summaryDeny}>
                              {' '}
                              -{' '}
                              {t('grant.denied', {
                                perms: grant.deny.map((permission) => permLabel[permission]).join(', ')
                              })}
                            </span>
                          ) : null}
                        </span>
                        {overlap.length > 0 ? (
                          <span className={styles.warning}>
                            <Icon name="warning" size={14} />
                            {t('grant.appears_both_allow_deny_so', {
                              perms: overlap.map((permission) => permLabel[permission]).join(', ')
                            })}
                          </span>
                        ) : null}
                        {expanded ? (
                          <span className={styles.perms}>
                            {grant.allow.map((permission) => (
                              <StowBadge key={`allow-${permission}`} tone="accent">
                                {permLabel[permission]}
                              </StowBadge>
                            ))}
                            {grant.deny.map((permission) => (
                              <StowBadge key={`deny-${permission}`} tone="danger">
                                {t('grant.denied', { perms: permLabel[permission] })}
                              </StowBadge>
                            ))}
                          </span>
                        ) : null}
                      </span>
                    }
                    trailing={
                      <span className={adminStyles.rowActions}>
                        <StowIconButton
                          label={
                            expanded ? t('grant.collapse_permission_details') : t('grant.expand_permission_details')
                          }
                          expanded={expanded}
                          onClick={() => toggleExpanded(grant.id)}
                        >
                          <span className={cx(styles.chevron, expanded && styles.chevronOpen)}>
                            <Icon name="chevron-right" size={18} />
                          </span>
                        </StowIconButton>
                        <StowIconButton
                          label={t('common.edit', { name: grantName })}
                          icon="settings"
                          onClick={() => void withPinned(grant, () => askGrant(principal, grant))}
                        />
                        <StowIconButton
                          label={t('common.remove', { name: grantName })}
                          icon="delete"
                          danger
                          onClick={() => void remove(grant)}
                        />
                      </span>
                    }
                  />
                )
              }}
            />
          )}
          <StowButton variant="tonal" icon={<Icon name="add" />} onClick={() => void askGrant(principal)}>
            {t('common.add_folder')}
          </StowButton>
        </>
      )}
    </section>
  )
}

/** Adds a folder grant, or edits one when given. Resolves true once saved. */
function askGrant(principal: GrantPrincipal, grant?: AdminGrant): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <GrantDialog principal={principal} grant={grant} open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface GrantValues {
  shareId: string
  subpath: string
  allow: GrantPermName[]
  deny: GrantPermName[]
  inherit: boolean
  label: string
}

interface GrantDialogProps {
  principal: GrantPrincipal
  grant?: AdminGrant
  open: boolean
  onDone: (saved: boolean) => void
  onClosed: () => void
}

function GrantDialog({ principal, grant, open, onDone, onClosed }: GrantDialogProps) {
  const { t } = useI18n()
  const shares = useAdminShares().data ?? []
  const create = useCreateGrant()
  const update = useUpdateGrant()
  const save = grant ? update : create
  const { control, handleSubmit, setError, setValue, formState } = useForm<GrantValues>({
    defaultValues: grant
      ? {
          shareId: String(grant.share),
          subpath: grant.subpath,
          allow: grant.allow,
          deny: grant.deny,
          inherit: grant.inherit,
          label: grant.label ?? ''
        }
      : {
          shareId: shares[0] ? String(shares[0].id) : '',
          subpath: '',
          allow: ['read', 'download'],
          deny: [],
          inherit: true,
          label: ''
        }
  })
  const [allow, deny] = useWatch({ control, name: ['allow', 'deny'] })
  const error =
    formState.errors.root?.message ??
    (save.error
      ? grantErrorText(save.error, grant ? t('common.could_not_save_change') : t('common.could_not_add_folder'))
      : null)

  const submit = handleSubmit((values) => {
    if (save.isPending) return
    if (!values.shareId) return setError('root', { message: t('grant.select_share') })
    if (values.allow.length === 0 && values.deny.length === 0)
      return setError('root', { message: t('grant.select_at_least_one_permission') })
    const done = { onSuccess: () => onDone(true) }
    if (grant)
      update.mutate(
        {
          id: grant.id,
          update: { allow: values.allow, deny: values.deny, inherit: values.inherit, label: values.label.trim() }
        },
        done
      )
    else
      create.mutate(
        {
          principal,
          share: Number(values.shareId),
          subpath: values.subpath.trim(),
          allow: values.allow,
          deny: values.deny,
          inherit: values.inherit,
          label: values.label.trim() || undefined
        },
        done
      )
  })
  const cancel = (): void => {
    if (!save.isPending) onDone(false)
  }

  return (
    <StowDialog
      open={open}
      title={grant ? t('grant.edit_folder_permission') : t('common.add_folder')}
      onClose={cancel}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" disabled={save.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton loading={save.isPending} onClick={() => void submit()}>
            {grant ? t('common.save') : t('common.add')}
          </StowButton>
        </>
      }
    >
      <form className={adminStyles.form} onSubmit={(event) => void submit(event)}>
        {grant ? (
          <p className={adminStyles.sectionFieldHint}>
            {shareNameOf(shares, grant.share)}
            {grant.subpath ? ` / ${grant.subpath}` : t('grant.root')}
            {t('grant.share_path_cannot_changed_grant')}
          </p>
        ) : (
          <>
            <Controller
              control={control}
              name="shareId"
              render={({ field }) => (
                <StowSelect
                  label={t('common.share')}
                  options={[
                    { value: '', label: t('grant.select_share'), disabled: true },
                    ...shares.map((share) => ({ value: String(share.id), label: share.name }))
                  ]}
                  value={field.value}
                  required
                  onChange={field.onChange}
                />
              )}
            />
            <StowFormTextField
              control={control}
              name="subpath"
              label={t('grant.subpath_leave_empty_whole_share')}
              placeholder={t('grant.e_g_vacation')}
              autoComplete="off"
            />
            <p className={adminStyles.sectionFieldHint}>{t('grant.left_empty_whole_share_appears')}</p>
          </>
        )}
        <GrantPermissionGrid
          allow={allow}
          deny={deny}
          onAllowChange={(next) => setValue('allow', next)}
          onDenyChange={(next) => setValue('deny', next)}
        />
        {grant && allow.some((permission) => deny.includes(permission)) ? (
          <p className={styles.warning}>
            <Icon name="warning" size={14} />
            {t('grant.permission_listed_both_allow_deny')}
          </p>
        ) : null}
        <Controller
          control={control}
          name="inherit"
          render={({ field }) => (
            <StowSwitch checked={field.value} label={t('grant.apply_subfolders')} onChange={field.onChange} />
          )}
        />
        <StowFormTextField
          control={control}
          name="label"
          label={t('grant.display_name_optional')}
          placeholder={t('grant.defaults_folder_name')}
          autoComplete="off"
        />
        {error ? (
          <p className={adminStyles.sectionError} role="alert">
            {error}
          </p>
        ) : null}
      </form>
    </StowDialog>
  )
}

function grantErrorText(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code === 'fs.invalid_name') return t('grant.select_at_least_one_permission')
  if (error instanceof ApiError && error.code === 'fs.not_found') return t('common.share_no_longer_exists')
  return describeApiError(error, fallback)
}
