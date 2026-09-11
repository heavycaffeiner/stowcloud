import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ALL_GRANT_PERMS, ApiError, type AdminGrant, type GrantPermName, type GrantPrincipal } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { adminGrantMutation, adminGrantsQuery, adminSharesQuery } from '../../query/admin'
import { useI18n } from '../../i18n/use-i18n'
import { Button } from '../Button'
import { Checkbox } from '../Checkbox'
import { Chip } from '../Chip'
import { Dialog } from '../Dialog'
import { Icon } from '../Icon'
import { IconButton } from '../IconButton'
import { ListItem } from '../ListItem'
import { ProgressCircular } from '../ProgressCircular'
import { Select, type SelectOption } from '../Select'
import { TextField } from '../TextField'
import './admin.css'

interface GrantManagementSectionProps {
  /** Who these grants belong to: a user id or a group id, never both. */
  principal: GrantPrincipal
  /** Display name used in the hint and delete confirmation copy. */
  label: string
}

type Translate = (key: string, params?: Record<string, string | number>) => string

type PermissionSets = {
  allow: Set<GrantPermName>
  deny: Set<GrantPermName>
}

export function GrantManagementSection({ principal, label }: GrantManagementSectionProps) {
  const { t } = useI18n()
  const permLabel: Record<GrantPermName, string> = {
    read: t('common.read'),
    write: t('grant.write'),
    create: t('grant.create'),
    delete: t('common.delete'),
    rename: t('grant.rename'),
    move: t('common.move'),
    share: t('common.share_links'),
    download: t('common.download')
  }
  const scope = principal.kind === 'user' ? { userId: principal.id } : { groupId: principal.id }
  const sharesQuery = useQuery(adminSharesQuery())
  const grantsQuery = useQuery(adminGrantsQuery(scope))
  const shares = sharesQuery.data ?? []
  const grants = grantsQuery.data ?? []
  const loading = sharesQuery.isPending || grantsQuery.isPending
  const loadError = sharesQuery.error || grantsQuery.error
    ? describeApiError(sharesQuery.error ?? grantsQuery.error, t('grant.could_not_load_permission_list'))
    : null

  const [expandedIds, setExpandedIds] = useState<Set<number>>(new Set())

  const [addOpen, setAddOpen] = useState(false)
  const [addShareId, setAddShareId] = useState('')
  const [addSubpath, setAddSubpath] = useState('')
  const [addAllow, setAddAllow] = useState<Set<GrantPermName>>(new Set(['read', 'download']))
  const [addDeny, setAddDeny] = useState<Set<GrantPermName>>(new Set())
  const [addInherit, setAddInherit] = useState(true)
  const [addLabel, setAddLabel] = useState('')
  const [addValidation, setAddValidation] = useState<string | null>(null)
  const addMut = useMutation(adminGrantMutation())

  const [editTarget, setEditTarget] = useState<AdminGrant | null>(null)
  const [editAllow, setEditAllow] = useState<Set<GrantPermName>>(new Set())
  const [editDeny, setEditDeny] = useState<Set<GrantPermName>>(new Set())
  const [editInherit, setEditInherit] = useState(true)
  const [editLabel, setEditLabel] = useState('')
  const [editValidation, setEditValidation] = useState<string | null>(null)
  const editMut = useMutation(adminGrantMutation())

  const [deleteTarget, setDeleteTarget] = useState<AdminGrant | null>(null)
  const deleteMut = useMutation(adminGrantMutation())

  const shareName = (id: number): string => shares.find((share) => share.id === id)?.name ?? t('grant.share', { id })
  const addError = addValidation ?? (addMut.error ? grantError(addMut.error, t('common.could_not_add_folder'), t) : null)
  const editError = editValidation ?? (editMut.error ? grantError(editMut.error, t('common.could_not_save_change'), t) : null)
  const deleteError = deleteMut.error ? describeApiError(deleteMut.error, t('common.could_not_remove')) : null
  const shareOptions: SelectOption[] = [
    { value: '', text: t('grant.select_share'), disabled: true },
    ...shares.map((share) => ({ value: String(share.id), text: share.name }))
  ]

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

  function openAdd(): void {
    addMut.reset()
    setAddValidation(null)
    setAddShareId(shares[0] ? String(shares[0].id) : '')
    setAddSubpath('')
    setAddAllow(new Set(['read', 'download']))
    setAddDeny(new Set())
    setAddInherit(true)
    setAddLabel('')
    setAddOpen(true)
  }

  function closeAdd(): void {
    if (!addMut.isPending) setAddOpen(false)
  }

  function submitAdd(): void {
    setAddValidation(null)
    if (!addShareId) {
      setAddValidation(t('grant.select_share'))
      return
    }
    if (addAllow.size === 0 && addDeny.size === 0) {
      setAddValidation(t('grant.select_at_least_one_permission'))
      return
    }
    addMut.mutate(
      {
        kind: 'create',
        req: {
          principal,
          share: Number(addShareId),
          subpath: addSubpath.trim(),
          allow: [...addAllow],
          deny: [...addDeny],
          inherit: addInherit,
          label: addLabel.trim() || undefined
        }
      },
      { onSuccess: () => setAddOpen(false) }
    )
  }

  function openEdit(grant: AdminGrant): void {
    editMut.reset()
    setEditValidation(null)
    setEditTarget(grant)
    setEditAllow(new Set(grant.allow))
    setEditDeny(new Set(grant.deny))
    setEditInherit(grant.inherit)
    setEditLabel(grant.label ?? '')
  }

  function closeEdit(): void {
    if (!editMut.isPending) setEditTarget(null)
  }

  function submitEdit(): void {
    if (!editTarget) return
    setEditValidation(null)
    if (editAllow.size === 0 && editDeny.size === 0) {
      setEditValidation(t('grant.select_at_least_one_permission'))
      return
    }
    editMut.mutate(
      {
        kind: 'update',
        id: editTarget.id,
        patch: {
          allow: [...editAllow],
          deny: [...editDeny],
          inherit: editInherit,
          label: editLabel.trim() || null
        }
      },
      { onSuccess: () => setEditTarget(null) }
    )
  }

  function askDelete(grant: AdminGrant): void {
    deleteMut.reset()
    setDeleteTarget(grant)
  }

  function closeDelete(): void {
    if (!deleteMut.isPending) setDeleteTarget(null)
  }

  function submitDelete(): void {
    if (!deleteTarget) return
    deleteMut.mutate({ kind: 'delete', id: deleteTarget.id }, { onSuccess: () => setDeleteTarget(null) })
  }

  return (
    <>
      <section className="sc-admin-section sc-grants">
        <p className="sc-admin-section__hint">
          <strong>{label}</strong>{t('grant.sees_only_folders_granted_here')}
        </p>
        {loading ? <ProgressCircular /> : loadError ? <p className="sc-admin-section__error" role="alert">{loadError}</p> : (
          <>
            {grants.length === 0 ? (
              <div className="sc-admin-empty">
                <Icon name="account_tree" />
                <p>{t('grant.no_folders_granted_yet_signing')}</p>
              </div>
            ) : (
              <ul className="sc-admin-list">
                {grants.map((grant) => {
                  const expanded = expandedIds.has(grant.id)
                  const overlap = grant.allow.filter((permission) => grant.deny.includes(permission))
                  const grantName = grant.label || shareName(grant.share)
                  return (
                    <li key={grant.id}>
                      <ListItem
                        headline={
                          <>
                            <span className="sc-admin-row__name">{grantName}</span>
                            {!grant.inherit ? <Chip variant="assist">{t('grant.path_only')}</Chip> : null}
                          </>
                        }
                        supporting={
                          <span className="sc-admin-grant__supporting">
                            <span>{shareName(grant.share)}{grant.subpath ? ` / ${grant.subpath}` : t('grant.root')}</span>
                            <span>
                              {allowSummary(grant)}
                              {grant.deny.length > 0 ? <span className="sc-admin-grant__summary-deny"> - {t('grant.denied', { perms: grant.deny.map((permission) => permLabel[permission]).join(', ') })}</span> : null}
                            </span>
                            {overlap.length > 0 ? (
                              <span className="sc-admin-grant__warning">
                                <Icon name="warning" size={14} />
                                {t('grant.appears_both_allow_deny_so', { perms: overlap.map((permission) => permLabel[permission]).join(', ') })}
                              </span>
                            ) : null}
                            {expanded ? (
                              <span className="sc-admin-grant__perms">
                                {grant.allow.map((permission) => <Chip key={`allow-${permission}`} variant="filter" selected>{permLabel[permission]}</Chip>)}
                                {grant.deny.map((permission) => <Chip key={`deny-${permission}`} variant="input">{t('grant.denied', { perms: permLabel[permission] })}</Chip>)}
                              </span>
                            ) : null}
                          </span>
                        }
                        trailing={
                          <span className="sc-admin-row__actions">
                            <IconButton
                              label={expanded ? t('grant.collapse_permission_details') : t('grant.expand_permission_details')}
                              expanded={expanded}
                              onClick={() => toggleExpanded(grant.id)}
                            >
                              <span className={`sc-admin-grant__chevron${expanded ? ' sc-admin-grant__chevron--open' : ''}`}>
                                <Icon name="chevron-right" size={18} />
                              </span>
                            </IconButton>
                            <IconButton label={t('common.edit', { name: grantName })} onClick={() => openEdit(grant)}>
                              <Icon name="settings" size={18} />
                            </IconButton>
                            <IconButton label={t('common.remove', { name: grantName })} onClick={() => askDelete(grant)}>
                              <Icon name="delete" size={18} />
                            </IconButton>
                          </span>
                        }
                      />
                    </li>
                  )
                })}
              </ul>
            )}
            <Button variant="tonal" icon={<Icon name="add" />} onClick={openAdd}>{t('common.add_folder')}</Button>
          </>
        )}
      </section>

      <Dialog
        open={addOpen}
        title={t('common.add_folder')}
        onClose={closeAdd}
        actions={
          <>
            <Button variant="text" disabled={addMut.isPending} onClick={closeAdd}>{t('common.cancel')}</Button>
            <Button loading={addMut.isPending} onClick={submitAdd}>{t('common.add')}</Button>
          </>
        }
      >
        <form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitAdd() }}>
          <Select label={t('common.share')} options={shareOptions} value={addShareId} required onValueChange={setAddShareId} />
          <TextField label={t('grant.subpath_leave_empty_whole_share')} placeholder={t('grant.e_g_vacation')} value={addSubpath} autoComplete="off" onValueChange={setAddSubpath} />
          <p className="sc-admin-section__field-hint">{t('grant.left_empty_whole_share_appears')}</p>
          <PermissionGrid allow={addAllow} deny={addDeny} setAllow={setAddAllow} setDeny={setAddDeny} permLabel={permLabel} t={t} />
          <Checkbox checked={addInherit} label={t('grant.apply_subfolders')} onchange={setAddInherit} />
          <TextField label={t('grant.display_name_optional')} placeholder={t('grant.defaults_folder_name')} value={addLabel} autoComplete="off" onValueChange={setAddLabel} />
          {addError ? <p className="sc-admin-section__error" role="alert">{addError}</p> : null}
        </form>
      </Dialog>

      <Dialog
        open={editTarget !== null}
        title={t('grant.edit_folder_permission')}
        onClose={closeEdit}
        actions={
          <>
            <Button variant="text" disabled={editMut.isPending} onClick={closeEdit}>{t('common.cancel')}</Button>
            <Button loading={editMut.isPending} onClick={submitEdit}>{t('common.save')}</Button>
          </>
        }
      >
        {editTarget ? (
          <form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitEdit() }}>
            <p className="sc-admin-section__field-hint">
              {shareName(editTarget.share)}{editTarget.subpath ? ` / ${editTarget.subpath}` : t('grant.root')}{t('grant.share_path_cannot_changed_grant')}
            </p>
            <PermissionGrid allow={editAllow} deny={editDeny} setAllow={setEditAllow} setDeny={setEditDeny} permLabel={permLabel} t={t} />
            {editAllow.size > 0 && [...editAllow].some((permission) => editDeny.has(permission)) ? (
              <p className="sc-admin-grant__warning">
                <Icon name="warning" size={14} />
                {t('grant.permission_listed_both_allow_deny')}
              </p>
            ) : null}
            <Checkbox checked={editInherit} label={t('grant.apply_subfolders')} onchange={setEditInherit} />
            <TextField label={t('grant.display_name_optional')} placeholder={t('grant.defaults_folder_name')} value={editLabel} autoComplete="off" onValueChange={setEditLabel} />
            {editError ? <p className="sc-admin-section__error" role="alert">{editError}</p> : null}
          </form>
        ) : null}
      </Dialog>

      <Dialog
        open={deleteTarget !== null}
        title={t('grant.remove_folder_permission')}
        onClose={closeDelete}
        actions={
          <>
            <Button variant="text" disabled={deleteMut.isPending} onClick={closeDelete}>{t('common.cancel')}</Button>
            <Button danger loading={deleteMut.isPending} onClick={submitDelete}>{t('common.remove_2')}</Button>
          </>
        }
      >
        <p>
          {t('grant.access_removed_immediately', { name: deleteTarget?.label || (deleteTarget ? shareName(deleteTarget.share) : '') })}{' '}
          {t('grant.will_not_see_folder_from', { principal: label })}
        </p>
        {deleteError ? <p className="sc-admin-section__error" role="alert">{deleteError}</p> : null}
      </Dialog>
    </>
  )
}

function PermissionGrid({ allow, deny, setAllow, setDeny, permLabel, t }: PermissionSets & {
  setAllow: (next: Set<GrantPermName>) => void
  setDeny: (next: Set<GrantPermName>) => void
  permLabel: Record<GrantPermName, string>
  t: Translate
}) {
  return (
    <div className="sc-admin-permgrid">
      <div className="sc-admin-permgrid__head"><span /> <span>{t('grant.allow')}</span><span>{t('grant.deny')}</span></div>
      {ALL_GRANT_PERMS.map((permission) => (
        <div className="sc-admin-permgrid__row" key={permission}>
          <span>{permLabel[permission]}</span>
          <span className="sc-admin-permgrid__cell">
            <Checkbox checked={allow.has(permission)} hideLabel label={t('grant.allow_2', { perm: permLabel[permission] })} onChange={(checked) => setAllow(togglePermission(allow, permission, checked))} />
          </span>
          <span className="sc-admin-permgrid__cell">
            <Checkbox checked={deny.has(permission)} hideLabel label={t('grant.deny_2', { perm: permLabel[permission] })} onChange={(checked) => setDeny(togglePermission(deny, permission, checked))} />
          </span>
        </div>
      ))}
    </div>
  )
}

function togglePermission(set: Set<GrantPermName>, permission: GrantPermName, checked: boolean): Set<GrantPermName> {
  const next = new Set(set)
  if (checked) next.add(permission)
  else next.delete(permission)
  return next
}

function grantError(error: unknown, fallback: string, t: Translate, mapNotFound = true): string {
  if (error instanceof ApiError && error.code === 'fs.invalid_name') return t('grant.select_at_least_one_permission')
  if (mapNotFound && error instanceof ApiError && error.code === 'fs.not_found') return t('common.share_no_longer_exists')
  return describeApiError(error, fallback)
}
