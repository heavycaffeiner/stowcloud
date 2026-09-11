import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError, type ActiveSession } from '../../api/client'
import { describeUserAgent } from '../../format/user-agent'
import { formatDateNs } from '../../i18n'
import { useI18n } from '../../i18n/use-i18n'
import { activeSessionsQuery, revokeSessionMutation } from '../../query/account'
import { Button } from '../Button'
import { SettingsDialog } from './SettingsDialog'

export function SessionsSection() {
  const { t } = useI18n()
  const list = useQuery(activeSessionsQuery())
  const revoke = useMutation(revokeSessionMutation())
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
    <div className="sc-sessions">
      {list.isPending ? <p>{t('common.loading')}</p> : list.isError ? <p className="sc-sessions__error">{t('common.could_not_load_list')}</p> : (
        <ul className="sc-sessions__list">
          {(list.data ?? []).map((session) => (
            <li key={session.id_hash}>
              <div>
                <strong>{session.ip_first ?? t('session.unknown_location')}</strong>
                {session.current ? <span className="sc-sessions__badge">{t('session.current_session')}</span> : null}
                <p title={session.ua_first ?? undefined}>{describeUserAgent(session.ua_first)} - {t('session.last_active', { date: formatDateNs(session.last_seen_ns) })}</p>
              </div>
              {!session.current ? <Button variant="text" onClick={() => setRevokeTarget(session)}>{t('session.sign_out_session')}</Button> : null}
            </li>
          ))}
        </ul>
      )}
      <SettingsDialog open={!!revokeTarget} title={t('session.sign_out_session_2')} onClose={() => setRevokeTarget(null)} actions={<><Button variant="text" onClick={() => setRevokeTarget(null)}>{t('common.cancel')}</Button><Button onClick={confirmRevoke} loading={revoke.isPending}>{t('common.sign_out')}</Button></>}>
        <p>{t('session.device_signed_out_immediately', { where: revokeTarget?.ip_first ?? t('session.unknown_location') })}</p>
      </SettingsDialog>
    </div>
  )
}
