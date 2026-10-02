import { useState } from 'react'
import { formatDateNs } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { useActiveSessions, useRevokeSession } from '../api'
import { Button } from '../../../ui/Button'
import { VirtualList } from '../../../ui/VirtualList'
import { SettingsDialog } from './SettingsDialog'
import * as styles from './SessionsSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../../api/fetcher'
import type { ActiveSession } from '../api'

export function SessionsSection() {
  const { t } = useI18n()
  const list = useActiveSessions()
  const revoke = useRevokeSession()
  const [revokeTarget, setRevokeTarget] = useState<ActiveSession | null>(null)

  function confirmRevoke(): void {
    if (!revokeTarget) return
    revoke.mutate(revokeTarget.id_hash, {
      onSuccess: () => setRevokeTarget(null),
      onError: (error) => {
        if (error instanceof ApiError && error.status === 404) {
          setRevokeTarget(null)
          void list.refetch()
        }
      }
    })
  }

  return (
    <div className={styles.root}>
      {list.isPending ? (
        <p className={settingsCardStyles.text}>{t('common.loading')}</p>
      ) : list.isError ? (
        <p className={styles.error}>{t('common.could_not_load_list')}</p>
      ) : (
        <VirtualList
          className={styles.list}
          items={list.data ?? []}
          itemKey={(session) => session.id_hash}
          estimateSize={88}
          itemProps={() => ({ className: styles.item })}
          renderItem={(session) => (
            <>
              <div className={styles.itemMain}>
                <strong>{session.ip_first ?? t('session.unknown_location')}</strong>
                {session.current ? <span className={styles.badge}>{t('session.current_session')}</span> : null}
                <p className={styles.detail} title={session.ua_first ?? undefined}>
                  {session.ua_display ?? t('session.unknown_device')} -{' '}
                  {t('session.last_active', { date: formatDateNs(session.last_seen_ns) })}
                </p>
              </div>
              {!session.current ? (
                <Button variant="text" onClick={() => setRevokeTarget(session)}>
                  {t('session.sign_out_session')}
                </Button>
              ) : null}
            </>
          )}
        />
      )}
      <SettingsDialog
        open={!!revokeTarget}
        title={t('session.sign_out_session_2')}
        onClose={() => setRevokeTarget(null)}
        actions={
          <>
            <Button variant="text" onClick={() => setRevokeTarget(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmRevoke} loading={revoke.isPending}>
              {t('common.sign_out')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>
          {t('session.device_signed_out_immediately', {
            where: revokeTarget?.ip_first ?? t('session.unknown_location')
          })}
        </p>
      </SettingsDialog>
    </div>
  )
}
