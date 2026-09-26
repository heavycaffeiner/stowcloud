import { useI18n } from '../../lib/i18n/use-i18n'
import { Button } from '../../lib/ui/Button'
import { Switch } from '../../lib/ui/Switch'
import { Dialog } from '../../lib/ui/Dialog'
import { Icon } from '../../lib/ui/Icon'
import { IconButton } from '../../lib/ui/IconButton'
import { ProgressCircular } from '../../lib/ui/ProgressCircular'
import { Select } from '../../lib/ui/Select'
import { TextField } from '../../lib/ui/TextField'
import { VirtualList } from '../../lib/ui/VirtualList'
import { formatDateNs } from '../../lib/i18n'
import { useShareManageController, isDropLink } from './share-manage-controller'
import './share-manage.css'

export interface ShareManageDialogProps {
  open: boolean
  path: string
  targetName: string
  targetIsDir: boolean
  onClose?: () => void
  onclose?: () => void
}

export function ShareManageDialog({ open, path, targetName, targetIsDir, onClose, onclose }: ShareManageDialogProps) {
  const { t } = useI18n()
  const closeParent = onClose ?? onclose ?? (() => undefined)
  const controller = useShareManageController(open, path, targetIsDir, t, closeParent)
  const { state, links, sharesQuery, createMut, updateMut, newKindOptions, newExpiryChoices, editExpiryChoices, newExpiryBad, editExpiryBad, createError, loadError, patch, openCreate, submitCreate, copyLink, acknowledgeIssued, closeIssued, openEdit, submitEdit, confirmRevoke } = controller
  const { dialogOpen, creatingOpen, newKind, newRead, newDownload, newPassword, newExpiry, newExpiryDate, newMaxDownloads, newLabel, justCreated, issuedAcknowledged, editingId, editRead, editDownload, editClearPassword, editNewPassword, editExpiry, editExpiryDate, editMaxDownloads, editLabel, revokeTarget, copiedId, copyErrorId } = state

  return (
    <>
      <Dialog className="sc-share-dialog" open={dialogOpen} title={t('share.share_links', { name: targetName })} onClose={closeIssued} role="dialog" closedby="any" actions={<Button variant="text" onClick={closeIssued}>{t('common.close')}</Button>}>
        {justCreated ? (
          <div className="sc-share__issued">
            <p className="sc-share__issued-note">{t('share.link_shown_only_now_cannot')}</p>
            <div className="sc-share__url-row">
              <textarea className="sc-share__url" readOnly rows={3} aria-label={t('share.copy_link')} value={justCreated.url ?? ''} />
              <IconButton label={t('share.copy_link')} onClick={() => void copyLink(justCreated.url ?? '', -1)}><Icon name="copy" /></IconButton>
            </div>
            {copiedId === -1 ? <p className="sc-share__copy-feedback" role="status">{t('common.copied')}</p> : null}
            {copyErrorId === -1 ? <p className="sc-share__copy-feedback sc-share__copy-feedback--error" role="alert">{t('share.copy_failed')}</p> : null}
            <Button variant="text" onClick={acknowledgeIssued}>{t('share.acknowledge_link_saved')}</Button>
          </div>
        ) : null}
        {sharesQuery.isPending ? <div className="sc-share__loading"><ProgressCircular /></div> : loadError ? <p className="sc-share__error" role="alert">{loadError}</p> : (
          <>
            {links.length === 0 && !creatingOpen ? <p className="sc-share__empty">{t('share.no_share_links_item')}</p> : null}
            <VirtualList
              className="sc-share__list"
              items={links}
              itemKey={(link) => link.id}
              estimateSize={112}
              itemProps={() => ({ className: 'sc-share__item' })}
              pinnedKeys={editingId === null ? [] : [editingId]}
              renderItem={(link) => (
                editingId === link.id ? (
                  <div className="sc-share__edit-form">
                    <div className="sc-share__perm-row">
                      <Switch checked={editRead} label={t('share.read_view')} onChange={(value) => patch({ editRead: value })} />
                      <Switch checked={editDownload} label={t('common.download')} onChange={(value) => patch({ editDownload: value })} />
                    </div>
                    <Select label={t('share.expiry')} options={editExpiryChoices} value={editExpiry} onValueChange={(value) => patch({ editExpiry: value })} />
                    {editExpiry === 'custom' ? <TextField type="date" label={t('share.expiry_date')} value={editExpiryDate} onValueChange={(value) => patch({ editExpiryDate: value })} error={editExpiryDate && editExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null} /> : null}
                    <TextField value={editMaxDownloads} label={t('share.download_limit_optional')} placeholder={t('share.no_limit')} onValueChange={(value) => patch({ editMaxDownloads: value })} />
                    <TextField value={editLabel} label={t('share.label_optional')} onValueChange={(value) => patch({ editLabel: value })} />
                    {link.has_password && !editClearPassword ? <Switch checked={editClearPassword} label={t('share.remove_password')} onChange={(value) => patch({ editClearPassword: value })} /> : null}
                    {!editClearPassword ? <TextField value={editNewPassword} type="password" label={t('share.new_password_optional')} onValueChange={(value) => patch({ editNewPassword: value })} /> : null}
                    <div className="sc-share__edit-actions"><Button variant="text" onClick={() => patch({ editingId: null })}>{t('common.cancel')}</Button><Button disabled={updateMut.isPending || editExpiryBad} onClick={() => void submitEdit(link)}>{t('common.save')}</Button></div>
                  </div>
                ) : (
                  <div className="sc-share__item-row">
                    <Icon name={link.has_password ? 'lock' : 'link'} size={18} />
                    <div className="sc-share__item-main">
                      <span className="sc-share__item-label">{link.label || t('share.no_label')}</span>
                      <span className="sc-share__item-meta">{isDropLink(link) ? t('share.kind_drop') : `${link.perms.read ? t('common.read') : ''}${link.perms.read && link.perms.download ? ' - ' : ''}${link.perms.download ? t('common.download') : ''} - ${t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })}`} {link.expires_ns ? `- ${t('share.expires', { date: formatDateNs(link.expires_ns) })}` : `- ${t('share.never_expires')}`}</span>
                      <span className="sc-share__item-meta">{t('share.created', { date: formatDateNs(link.created_ns) })}</span>
                    </div>
                    <div className="sc-share__item-actions">
                      {link.url ? <IconButton label={copiedId === link.id ? t('share.copied') : t('share.copy_link')} onClick={() => void copyLink(link.url ?? '', link.id)}><Icon name={copiedId === link.id ? 'check' : 'copy'} /></IconButton> : null}
                      <IconButton label={t('share.edit')} onClick={() => openEdit(link)}><Icon name="rename" /></IconButton>
                      <IconButton label={t('share.revoke')} onClick={() => patch({ revokeTarget: link })}><Icon name="close" /></IconButton>
                    </div>
                  </div>
                )
              )}
            />
            {creatingOpen ? (
              <div className="sc-share__create-form">
                <h3>{t('share.create_new_link')}</h3>
                <Select label={t('share.kind_label')} options={newKindOptions} value={newKind} onValueChange={(value) => patch({ newKind: value as 'download' | 'drop' })} />
                {newKind === 'drop' ? <p className="sc-share__hint">{t('share.drop_hint')}</p> : <div className="sc-share__perm-row"><Switch checked={newRead} label={t('share.read_view')} onChange={(value) => patch({ newRead: value })} /><Switch checked={newDownload} label={t('common.download')} onChange={(value) => patch({ newDownload: value })} /></div>}
                <Select label={t('share.expiry')} options={newExpiryChoices} value={newExpiry} onValueChange={(value) => patch({ newExpiry: value })} />
                {newExpiry === 'custom' ? <TextField type="date" label={t('share.expiry_date')} value={newExpiryDate} onValueChange={(value) => patch({ newExpiryDate: value })} error={newExpiryDate && newExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null} /> : null}
                <TextField value={newPassword} type="password" label={t('share.password_optional')} onValueChange={(value) => patch({ newPassword: value })} />
                {newKind !== 'drop' ? <TextField value={newMaxDownloads} label={t('share.download_limit_optional')} placeholder={t('share.no_limit')} onValueChange={(value) => patch({ newMaxDownloads: value })} /> : null}
                <TextField value={newLabel} label={t('share.label_optional')} placeholder={targetName} onValueChange={(value) => patch({ newLabel: value })} />
                {createError ? <p className="sc-share__error" role="alert">{createError}</p> : null}
                <div className="sc-share__edit-actions"><Button variant="text" onClick={() => patch({ creatingOpen: false })}>{t('common.cancel')}</Button><Button disabled={createMut.isPending || newExpiryBad || (newKind !== 'drop' && !newRead && !newDownload)} onClick={() => void submitCreate()}>{t('common.create')}</Button></div>
              </div>
            ) : <Button variant="tonal" onClick={openCreate} icon={<Icon name="add" size={18} />}>{t('share.create_new_link')}</Button>}
          </>
        )}
      </Dialog>
      <Dialog className="sc-share-dialog" open={revokeTarget !== null} title={t('share.revoke_share_link')} onClose={() => patch({ revokeTarget: null })} actions={<><Button variant="text" onClick={() => patch({ revokeTarget: null })}>{t('common.cancel')}</Button><Button danger onClick={() => void confirmRevoke()}>{t('share.revoke')}</Button></>}>
        <p>{t('share.link_stops_working_cannot_undone')}</p>
      </Dialog>
    </>
  )
}
