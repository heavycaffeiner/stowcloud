import { t } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { Button } from '../../ui/Button'
import { Icon } from '../../ui/Icon'
import { IconButton } from '../../ui/IconButton'
import { ListItem } from '../../ui/ListItem'
import { Switch } from '../../ui/Switch'
import { VirtualList } from '../../ui/VirtualList'
import * as styles from './ShareManagementList.css'
import * as adminStyles from './admin.css'
import * as buttonStyles from '../../ui/Button.css'
import type { AdminShare, ShareBackend } from './api'
import type { ShareEncryption } from '../shares/api'

interface ShareManagementListProps {
  shares: AdminShare[]
  encryptionByShare: Map<number, ShareEncryption>
  encryptionLoaded: boolean
  pinnedKeys: number[]
  trashTogglingId: number | null
  retryingId: number | null
  onEnableEncryption: (share: AdminShare) => void
  onDisableEncryption: (share: AdminShare) => void
  onCopySalt: (salt: string, name: string) => void
  onToggleTrash: (share: AdminShare, enabled: boolean) => void
  onRetry: (share: AdminShare) => void
  onEdit: (share: AdminShare) => void
  onDelete: (share: AdminShare) => void
}

export function backendLabel(backend: ShareBackend): string {
  if (backend === 's3') return t('folder_share.backend_s3')
  if (backend === 'veracrypt') return t('folder_share.backend_veracrypt')
  return t('folder_share.backend_local')
}

function brokenText(reason?: string): string {
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

export function ShareManagementList({
  shares,
  encryptionByShare,
  encryptionLoaded,
  pinnedKeys,
  trashTogglingId,
  retryingId,
  onEnableEncryption,
  onDisableEncryption,
  onCopySalt,
  onToggleTrash,
  onRetry,
  onEdit,
  onDelete
}: ShareManagementListProps) {
  const { t } = useI18n()
  return shares.length === 0 ? (
    <div className={styles.empty}>
      <Icon name="folder-tree" size={28} />
      <p className={styles.emptyText}>{t('folder_share.no_shares_registered_add_folder')}</p>
    </div>
  ) : (
    <VirtualList
      className={styles.list}
      items={shares}
      itemKey={(share) => share.id}
      estimateSize={112}
      itemProps={() => ({ className: styles.item })}
      pinnedKeys={pinnedKeys}
      renderItem={(share) => {
        const encryption = encryptionByShare.get(share.id)
        return (
          <ListItem
            className={styles.row}
            leading={<Icon name="folder" size={20} />}
            headline={
              <>
                <span>{share.name}</span>
                {share.backend !== 'local' ? (
                  <small className={styles.shareBackend}>{backendLabel(share.backend)}</small>
                ) : null}
              </>
            }
            supporting={
              <>
                <code data-testid="share-source">{share.source}</code>
                {share.broken_reason ? (
                  <span className={adminStyles.error}>{brokenText(share.broken_reason)}</span>
                ) : null}
                {encryptionLoaded ? (
                  <span className={styles.enc} data-testid="share-encryption">
                    {encryption ? (
                      <>
                        <span className={styles.encNote}>
                          <Icon name="lock" size={14} />
                          {t('encryption.encrypted_note')}
                        </span>
                        <Button
                          variant="text"
                          ariaLabel={t('encryption.disable_title', { name: share.name })}
                          onClick={() => onDisableEncryption(share)}
                        >
                          {t('encryption.disable')}
                        </Button>
                      </>
                    ) : share.empty ? (
                      <Button
                        variant="text"
                        ariaLabel={t('encryption.enable_title', { name: share.name })}
                        onClick={() => onEnableEncryption(share)}
                      >
                        {t('encryption.enable')}
                      </Button>
                    ) : null}
                  </span>
                ) : null}
                {encryption ? (
                  <span className={styles.encSaltRow}>
                    <span className={styles.encSaltLabel}>{t('encryption.salt_label')}</span>
                    <code className={styles.encSalt} data-testid="share-encryption-salt">
                      {encryption.salt}
                    </code>
                    <Button
                      variant="text"
                      ariaLabel={t('encryption.copy_salt', { name: share.name })}
                      onClick={() => onCopySalt(encryption.salt, share.name)}
                    >
                      {t('common.copy')}
                    </Button>
                  </span>
                ) : null}
              </>
            }
            trailing={
              <>
                <span
                  className={styles.trash}
                  title={trashTogglingId === share.id ? t('folder_share.applying') : undefined}
                >
                  <span className={styles.trashLabel}>{t('folder_share.use_trash')}</span>
                  <Switch
                    checked={share.trash_enabled}
                    disabled={trashTogglingId === share.id}
                    label={t('folder_share.trash', { name: share.name })}
                    showLabel={false}
                    onChange={(enabled) => onToggleTrash(share, enabled)}
                  />
                </span>
                {share.broken_reason ? (
                  <Button variant="tonal" loading={retryingId === share.id} onClick={() => onRetry(share)}>
                    {t('folder_share.retry')}
                  </Button>
                ) : null}
                <IconButton
                  label={t('common.edit', { name: share.name })}
                  icon="rename"
                  onClick={() => onEdit(share)}
                />
                <span className={buttonStyles.danger}>
                  <IconButton
                    label={t('common.remove', { name: share.name })}
                    icon="delete"
                    onClick={() => onDelete(share)}
                  />
                </span>
              </>
            }
          />
        )
      }}
    />
  )
}
