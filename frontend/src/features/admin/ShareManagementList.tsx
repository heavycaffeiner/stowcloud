import type { AdminShare, ShareBackend, ShareEncryption } from '../../lib/api/client'
import { Button } from '../../lib/ui/Button'
import { Icon } from '../../lib/ui/Icon'
import { IconButton } from '../../lib/ui/IconButton'
import { ListItem } from '../../lib/ui/ListItem'
import { Switch } from '../../lib/ui/Switch'
import { VirtualList } from '../../lib/ui/VirtualList'

type Translator = (key: string, params?: Record<string, string | number>) => string

interface ShareManagementListProps {
  t: Translator
  shares: AdminShare[]
  encryptionByShare: Map<number, ShareEncryption>
  encryptionLoaded: boolean
  pinnedKeys: number[]
  trashTogglingId: number | null
  retryingId: number | null
  trashError: string | null
  retryError: string | null
  onEnableEncryption: (share: AdminShare) => void
  onDisableEncryption: (share: AdminShare) => void
  onCopySalt: (salt: string, name: string) => void
  onToggleTrash: (share: AdminShare, enabled: boolean) => void
  onRetry: (share: AdminShare) => void
  onEdit: (share: AdminShare) => void
  onDelete: (share: AdminShare) => void
}

function backendLabel(t: Translator, backend: ShareBackend): string {
  if (backend === 's3') return t('folder_share.backend_s3')
  if (backend === 'veracrypt') return t('folder_share.backend_veracrypt')
  return t('folder_share.backend_local')
}

function brokenText(t: Translator, reason?: string): string {
  switch (reason) {
    case 'missing': return t('folder_share.broken_missing')
    case 'unreadable': return t('folder_share.broken_unreadable')
    case 'passphrase': return t('folder_share.broken_passphrase')
    case 'container_corrupt': return t('folder_share.broken_container_corrupt')
    case 'container_filesystem': return t('folder_share.broken_container_filesystem')
    case 'container_unsupported': return t('folder_share.broken_container_unsupported')
    default: return t('folder_share.broken_unavailable')
  }
}

export function ShareManagementList({ t, shares, encryptionByShare, encryptionLoaded, pinnedKeys, trashTogglingId, retryingId, trashError, retryError, onEnableEncryption, onDisableEncryption, onCopySalt, onToggleTrash, onRetry, onEdit, onDelete }: ShareManagementListProps) {
  return (
    <>
      {shares.length === 0 ? (
        <div className="sc-shares-empty">
          <Icon name="folder-tree" size={28} />
          <p>{t('folder_share.no_shares_registered_add_folder')}</p>
        </div>
      ) : (
        <VirtualList
          className="sc-shares-list"
          items={shares}
          itemKey={(share) => share.id}
          estimateSize={112}
          pinnedKeys={pinnedKeys}
          renderItem={(share) => {
            const encryption = encryptionByShare.get(share.id)
            return (
              <ListItem
                leading={<Icon name="folder" size={20} />}
                headline={<><span>{share.name}</span>{share.backend !== 'local' ? <small className="sc-share-backend">{backendLabel(t, share.backend)}</small> : null}</>}
                supporting={(
                  <>
                    <code data-testid="share-source">{share.source}</code>
                    {share.broken_reason ? <span className="sc-admin-error">{brokenText(t, share.broken_reason)}</span> : null}
                    {encryptionLoaded ? (
                      <span className="sc-shares-enc" data-testid="share-encryption">
                        {encryption ? (
                          <>
                            <span className="sc-shares-enc-note"><Icon name="lock" size={14} />{t('encryption.encrypted_note')}</span>
                            <Button variant="text" ariaLabel={t('encryption.disable_title', { name: share.name })} onClick={() => onDisableEncryption(share)}>{t('encryption.disable')}</Button>
                          </>
                        ) : share.empty ? (
                          <Button variant="text" ariaLabel={t('encryption.enable_title', { name: share.name })} onClick={() => onEnableEncryption(share)}>{t('encryption.enable')}</Button>
                        ) : null}
                      </span>
                    ) : null}
                    {encryption ? (
                      <span className="sc-shares-enc-salt-row">
                        <span className="sc-shares-enc-salt-label">{t('encryption.salt_label')}</span>
                        <code className="sc-shares-enc-salt" data-testid="share-encryption-salt">{encryption.salt}</code>
                        <Button variant="text" ariaLabel={t('encryption.copy_salt', { name: share.name })} onClick={() => onCopySalt(encryption.salt, share.name)}>{t('common.copy')}</Button>
                      </span>
                    ) : null}
                  </>
                )}
                trailing={(
                  <>
                    <span className="sc-shares-trash" title={trashTogglingId === share.id ? t('folder_share.applying') : undefined}>
                      <span className="sc-shares-trash-label">{t('folder_share.use_trash')}</span>
                      <Switch checked={share.trash_enabled} disabled={trashTogglingId === share.id} label={t('folder_share.trash', { name: share.name })} showLabel={false} onChange={(enabled) => onToggleTrash(share, enabled)} />
                    </span>
                    {share.broken_reason ? <Button variant="tonal" loading={retryingId === share.id} onClick={() => onRetry(share)}>{t('folder_share.retry')}</Button> : null}
                    <IconButton label={t('common.edit', { name: share.name })} icon="rename" onClick={() => onEdit(share)} />
                    <span className="sc-danger"><IconButton label={t('common.remove', { name: share.name })} icon="delete" onClick={() => onDelete(share)} /></span>
                  </>
                )}
              />
            )
          }}
        />
      )}
      {trashError ? <p className="sc-admin-error" role="alert">{trashError}</p> : null}
      {retryError ? <p className="sc-admin-error" role="alert">{retryError}</p> : null}
    </>
  )
}
