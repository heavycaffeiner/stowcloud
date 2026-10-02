import { useEffect, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { overlay } from 'overlay-kit'
import { describeApiError } from '../../../api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { formatDateNs, t } from '../../../i18n'
import { confirmAction } from '../../../ui/ActionDialog'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { Icon } from '../../../ui/Icon'
import { IconButton } from '../../../ui/IconButton'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import { VirtualList } from '../../../ui/VirtualList'
import { cx } from '../../../ui/cx'
import { useDeleteShareLink, useShareLinks, type ShareLinkInfo } from '../../links/api'
import { EditLinkForm, NewLinkForm } from './ShareLinkForms'
import * as styles from './ShareManageDialog.css'

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

async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fall through to the selectable textarea fallback.
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.append(area)
  area.select()
  let copied = false
  try {
    copied = document.execCommand('copy')
  } catch {
    copied = false
  }
  area.remove()
  return copied
}

/** Copies text. `isSuccess` holds for two seconds after a copy. */
function useCopyFlash() {
  const copy = useMutation({
    mutationFn: async (text: string) => {
      if (!(await copyText(text))) throw new Error('copy failed')
    }
  })
  const { isSuccess, reset } = copy
  useEffect(() => {
    if (!isSuccess) return
    const timer = window.setTimeout(reset, 2000)
    return () => window.clearTimeout(timer)
  }, [isSuccess, reset])
  return copy
}

const isDropLink = (link: ShareLinkInfo): boolean =>
  Boolean(link.perms.create && !link.perms.read && !link.perms.download)

function ShareManageDialog({ open, path, targetName, targetIsDir, onClose, onClosed }: ShareManageDialogProps) {
  const { t } = useI18n()
  const links = useShareLinks(path)
  const remove = useDeleteShareLink()
  // A new link's URL is shown only once, so closing waits until it is acknowledged.
  const [issued, setIssued] = useState<ShareLinkInfo | null>(null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [creating, setCreating] = useState(false)
  const close = (): void => {
    if (!issued) onClose()
  }
  async function revoke(link: ShareLinkInfo): Promise<void> {
    const revoked = await confirmAction({
      title: t('share.revoke_share_link'),
      body: <p>{t('share.link_stops_working_cannot_undone')}</p>,
      action: t('share.revoke'),
      danger: true,
      run: () => remove.mutateAsync(link.id).then(() => true),
      describeError: (error) => describeApiError(error, t('share.could_not_revoke_share_link'))
    })
    if (revoked) setIssued((current) => (current?.id === link.id ? null : current))
  }

  return (
    <Dialog
      size="wide"
      open={open}
      title={t('share.share_links', { name: targetName })}
      onClose={close}
      onClosed={onClosed}
      role="dialog"
      actions={
        <Button variant="text" onClick={close}>
          {t('common.close')}
        </Button>
      }
    >
      {issued ? <IssuedLink link={issued} onAcknowledge={() => setIssued(null)} /> : null}
      {links.isPending ? (
        <div className={styles.loading}>
          <ProgressCircular />
        </div>
      ) : links.error ? (
        <p className={styles.error} role="alert">
          {describeApiError(links.error, t('share.could_not_load_share_links'))}
        </p>
      ) : (
        <>
          {links.data.length === 0 && !creating ? (
            <p className={styles.empty}>{t('share.no_share_links_item')}</p>
          ) : null}
          <VirtualList
            className={styles.list}
            items={links.data}
            itemKey={(link) => link.id}
            estimateSize={112}
            itemProps={() => ({ className: styles.item })}
            pinnedKeys={editingId === null ? [] : [editingId]}
            renderItem={(link) =>
              editingId === link.id ? (
                <EditLinkForm link={link} onDone={() => setEditingId(null)} />
              ) : (
                <LinkSummary link={link} onEdit={() => setEditingId(link.id)} onRevoke={() => void revoke(link)} />
              )
            }
          />
          {creating ? (
            <NewLinkForm
              path={path}
              targetName={targetName}
              targetIsDir={targetIsDir}
              onCreated={(link) => {
                setIssued(link)
                setCreating(false)
              }}
              onCancel={() => setCreating(false)}
            />
          ) : (
            <Button variant="tonal" onClick={() => setCreating(true)} icon={<Icon name="add" size={18} />}>
              {t('share.create_new_link')}
            </Button>
          )}
        </>
      )}
    </Dialog>
  )
}

function IssuedLink({ link, onAcknowledge }: { link: ShareLinkInfo; onAcknowledge: () => void }) {
  const { t } = useI18n()
  const copy = useCopyFlash()
  return (
    <div className={styles.issued}>
      <p className={styles.issuedNote}>{t('share.link_shown_only_now_cannot')}</p>
      <div className={styles.urlRow}>
        <textarea className={styles.url} readOnly rows={3} aria-label={t('share.copy_link')} value={link.url ?? ''} />
        <IconButton label={t('share.copy_link')} onClick={() => copy.mutate(link.url ?? '')}>
          <Icon name="copy" />
        </IconButton>
      </div>
      {copy.isSuccess ? (
        <p className={styles.copyFeedback} role="status">
          {t('common.copied')}
        </p>
      ) : null}
      {copy.isError ? (
        <p className={cx(styles.copyFeedback, styles.copyFeedbackError)} role="alert">
          {t('share.copy_failed')}
        </p>
      ) : null}
      <Button variant="text" onClick={onAcknowledge}>
        {t('share.acknowledge_link_saved')}
      </Button>
    </div>
  )
}

interface LinkSummaryProps {
  link: ShareLinkInfo
  onEdit: () => void
  onRevoke: () => void
}

function LinkSummary({ link, onEdit, onRevoke }: LinkSummaryProps) {
  const { t } = useI18n()
  const copy = useCopyFlash()
  return (
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
        <span className={styles.itemMeta}>{t('share.created', { date: formatDateNs(link.created_ns) })}</span>
      </div>
      <div className={styles.itemActions}>
        {link.url ? (
          <IconButton
            label={copy.isSuccess ? t('share.copied') : t('share.copy_link')}
            onClick={() => copy.mutate(link.url ?? '')}
          >
            <Icon name={copy.isSuccess ? 'check' : 'copy'} />
          </IconButton>
        ) : null}
        <IconButton label={t('share.edit')} onClick={onEdit}>
          <Icon name="rename" />
        </IconButton>
        <IconButton label={t('share.revoke')} onClick={onRevoke}>
          <Icon name="close" />
        </IconButton>
      </div>
    </div>
  )
}
