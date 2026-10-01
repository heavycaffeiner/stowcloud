import { overlay } from 'overlay-kit'
import { useI18n } from '../../hooks/use-i18n'
import { Button } from '../../ui/Button'
import { Switch } from '../../ui/Switch'
import { Dialog } from '../../ui/Dialog'
import { Icon } from '../../ui/Icon'
import { IconButton } from '../../ui/IconButton'
import { ProgressCircular } from '../../ui/ProgressCircular'
import { Select } from '../../ui/Select'
import { TextField } from '../../ui/TextField'
import { VirtualList } from '../../ui/VirtualList'
import { formatDateNs, t } from '../../lib/i18n'
import type { ShareLinkInfo } from '../links/api'
import { useShareManageController, isDropLink } from './hooks/share-manage-controller'
import * as styles from './ShareManageDialog.css'
import { cx } from '../../ui/cx'

export interface ShareTarget {
  path: string
  targetName: string
  targetIsDir: boolean
}

interface ShareManageDialogProps extends ShareTarget {
  open: boolean
  onClose: () => void
  onClosed: () => void
}

/** Opens the share manager for one file or folder. Settles once it has closed. */
export function openShareManager(target: ShareTarget): Promise<void> {
  return overlay.openAsync<void>(({ isOpen, close, unmount }) => (
    <ShareManageDialog {...target} open={isOpen} onClose={() => close()} onClosed={unmount} />
  ))
}

function confirmRevoke(): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <Dialog
      size="wide"
      open={isOpen}
      title={t('share.revoke_share_link')}
      onClose={() => close(false)}
      onClosed={unmount}
      actions={
        <>
          <Button variant="text" onClick={() => close(false)}>
            {t('common.cancel')}
          </Button>
          <Button danger onClick={() => close(true)}>
            {t('share.revoke')}
          </Button>
        </>
      }
    >
      <p>{t('share.link_stops_working_cannot_undone')}</p>
    </Dialog>
  ))
}

function ShareManageDialog({ open, path, targetName, targetIsDir, onClose, onClosed }: ShareManageDialogProps) {
  const { t } = useI18n()
  const controller = useShareManageController(path, targetIsDir, t, onClose)
  const {
    state,
    links,
    sharesQuery,
    createMut,
    updateMut,
    newKindOptions,
    newExpiryChoices,
    editExpiryChoices,
    newExpiryBad,
    editExpiryBad,
    createError,
    loadError,
    patch,
    openCreate,
    submitCreate,
    copyLink,
    acknowledgeIssued,
    closeIssued,
    openEdit,
    submitEdit,
    revoke
  } = controller
  const {
    creatingOpen,
    newKind,
    newRead,
    newDownload,
    newPassword,
    newExpiry,
    newExpiryDate,
    newMaxDownloads,
    newLabel,
    justCreated,
    issuedAcknowledged,
    editingId,
    editRead,
    editDownload,
    editClearPassword,
    editNewPassword,
    editExpiry,
    editExpiryDate,
    editMaxDownloads,
    editLabel,
    copiedId,
    copyErrorId
  } = state
  const askRevoke = async (link: ShareLinkInfo): Promise<void> => {
    if (await confirmRevoke()) await revoke(link)
  }

  return (
    <Dialog
      size="wide"
      open={open}
      title={t('share.share_links', { name: targetName })}
      onClose={closeIssued}
      onClosed={onClosed}
      role="dialog"
      actions={
        <Button variant="text" onClick={closeIssued}>
          {t('common.close')}
        </Button>
      }
    >
      {justCreated ? (
        <div className={styles.issued}>
          <p className={styles.issuedNote}>{t('share.link_shown_only_now_cannot')}</p>
          <div className={styles.urlRow}>
            <textarea
              className={styles.url}
              readOnly
              rows={3}
              aria-label={t('share.copy_link')}
              value={justCreated.url ?? ''}
            />
            <IconButton label={t('share.copy_link')} onClick={() => void copyLink(justCreated.url ?? '', -1)}>
              <Icon name="copy" />
            </IconButton>
          </div>
          {copiedId === -1 ? (
            <p className={styles.copyFeedback} role="status">
              {t('common.copied')}
            </p>
          ) : null}
          {copyErrorId === -1 ? (
            <p className={cx(styles.copyFeedback, styles.copyFeedbackError)} role="alert">
              {t('share.copy_failed')}
            </p>
          ) : null}
          <Button variant="text" onClick={acknowledgeIssued}>
            {t('share.acknowledge_link_saved')}
          </Button>
        </div>
      ) : null}
      {sharesQuery.isPending ? (
        <div className={styles.loading}>
          <ProgressCircular />
        </div>
      ) : loadError ? (
        <p className={styles.error} role="alert">
          {loadError}
        </p>
      ) : (
        <>
          {links.length === 0 && !creatingOpen ? (
            <p className={styles.empty}>{t('share.no_share_links_item')}</p>
          ) : null}
          <VirtualList
            className={styles.list}
            items={links}
            itemKey={(link) => link.id}
            estimateSize={112}
            itemProps={() => ({ className: styles.item })}
            pinnedKeys={editingId === null ? [] : [editingId]}
            renderItem={(link) =>
              editingId === link.id ? (
                <div className={styles.editForm}>
                  <div className={styles.permRow}>
                    <Switch
                      checked={editRead}
                      label={t('share.read_view')}
                      onChange={(value) => patch({ editRead: value })}
                    />
                    <Switch
                      checked={editDownload}
                      label={t('common.download')}
                      onChange={(value) => patch({ editDownload: value })}
                    />
                  </div>
                  <Select
                    label={t('share.expiry')}
                    options={editExpiryChoices}
                    value={editExpiry}
                    onValueChange={(value) => patch({ editExpiry: value })}
                  />
                  {editExpiry === 'custom' ? (
                    <TextField
                      type="date"
                      label={t('share.expiry_date')}
                      value={editExpiryDate}
                      onValueChange={(value) => patch({ editExpiryDate: value })}
                      error={editExpiryDate && editExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null}
                    />
                  ) : null}
                  <TextField
                    value={editMaxDownloads}
                    label={t('share.download_limit_optional')}
                    placeholder={t('share.no_limit')}
                    onValueChange={(value) => patch({ editMaxDownloads: value })}
                  />
                  <TextField
                    value={editLabel}
                    label={t('share.label_optional')}
                    onValueChange={(value) => patch({ editLabel: value })}
                  />
                  {link.has_password && !editClearPassword ? (
                    <Switch
                      checked={editClearPassword}
                      label={t('share.remove_password')}
                      onChange={(value) => patch({ editClearPassword: value })}
                    />
                  ) : null}
                  {!editClearPassword ? (
                    <TextField
                      value={editNewPassword}
                      type="password"
                      label={t('share.new_password_optional')}
                      onValueChange={(value) => patch({ editNewPassword: value })}
                    />
                  ) : null}
                  <div className={styles.editActions}>
                    <Button variant="text" onClick={() => patch({ editingId: null })}>
                      {t('common.cancel')}
                    </Button>
                    <Button disabled={updateMut.isPending || editExpiryBad} onClick={() => void submitEdit(link)}>
                      {t('common.save')}
                    </Button>
                  </div>
                </div>
              ) : (
                <div className={styles.itemRow}>
                  <Icon name={link.has_password ? 'lock' : 'link'} size={18} />
                  <div className={styles.itemMain}>
                    <span className={styles.itemLabel}>{link.label || t('share.no_label')}</span>
                    <span className={styles.itemMeta}>
                      {isDropLink(link)
                        ? t('share.kind_drop')
                        : `${link.perms.read ? t('common.read') : ''}${link.perms.read && link.perms.download ? ' - ' : ''}${link.perms.download ? t('common.download') : ''} - ${t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })}`}{' '}
                      {link.expires_ns
                        ? `- ${t('share.expires', { date: formatDateNs(link.expires_ns) })}`
                        : `- ${t('share.never_expires')}`}
                    </span>
                    <span className={styles.itemMeta}>
                      {t('share.created', { date: formatDateNs(link.created_ns) })}
                    </span>
                  </div>
                  <div className={styles.itemActions}>
                    {link.url ? (
                      <IconButton
                        label={copiedId === link.id ? t('share.copied') : t('share.copy_link')}
                        onClick={() => void copyLink(link.url ?? '', link.id)}
                      >
                        <Icon name={copiedId === link.id ? 'check' : 'copy'} />
                      </IconButton>
                    ) : null}
                    <IconButton label={t('share.edit')} onClick={() => openEdit(link)}>
                      <Icon name="rename" />
                    </IconButton>
                    <IconButton label={t('share.revoke')} onClick={() => void askRevoke(link)}>
                      <Icon name="close" />
                    </IconButton>
                  </div>
                </div>
              )
            }
          />
          {creatingOpen ? (
            <div className={styles.createForm}>
              <h3 className={styles.createTitle}>{t('share.create_new_link')}</h3>
              <Select
                label={t('share.kind_label')}
                options={newKindOptions}
                value={newKind}
                onValueChange={(value) => patch({ newKind: value as 'download' | 'drop' })}
              />
              {newKind === 'drop' ? (
                <p className={styles.hint}>{t('share.drop_hint')}</p>
              ) : (
                <div className={styles.permRow}>
                  <Switch
                    checked={newRead}
                    label={t('share.read_view')}
                    onChange={(value) => patch({ newRead: value })}
                  />
                  <Switch
                    checked={newDownload}
                    label={t('common.download')}
                    onChange={(value) => patch({ newDownload: value })}
                  />
                </div>
              )}
              <Select
                label={t('share.expiry')}
                options={newExpiryChoices}
                value={newExpiry}
                onValueChange={(value) => patch({ newExpiry: value })}
              />
              {newExpiry === 'custom' ? (
                <TextField
                  type="date"
                  label={t('share.expiry_date')}
                  value={newExpiryDate}
                  onValueChange={(value) => patch({ newExpiryDate: value })}
                  error={newExpiryDate && newExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null}
                />
              ) : null}
              <TextField
                value={newPassword}
                type="password"
                label={t('share.password_optional')}
                onValueChange={(value) => patch({ newPassword: value })}
              />
              {newKind !== 'drop' ? (
                <TextField
                  value={newMaxDownloads}
                  label={t('share.download_limit_optional')}
                  placeholder={t('share.no_limit')}
                  onValueChange={(value) => patch({ newMaxDownloads: value })}
                />
              ) : null}
              <TextField
                value={newLabel}
                label={t('share.label_optional')}
                placeholder={targetName}
                onValueChange={(value) => patch({ newLabel: value })}
              />
              {createError ? (
                <p className={styles.error} role="alert">
                  {createError}
                </p>
              ) : null}
              <div className={styles.editActions}>
                <Button variant="text" onClick={() => patch({ creatingOpen: false })}>
                  {t('common.cancel')}
                </Button>
                <Button
                  disabled={createMut.isPending || newExpiryBad || (newKind !== 'drop' && !newRead && !newDownload)}
                  onClick={() => void submitCreate()}
                >
                  {t('common.create')}
                </Button>
              </div>
            </div>
          ) : (
            <Button variant="tonal" onClick={openCreate} icon={<Icon name="add" size={18} />}>
              {t('share.create_new_link')}
            </Button>
          )}
        </>
      )}
    </Dialog>
  )
}
