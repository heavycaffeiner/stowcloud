import { useI18n } from '../../../hooks/use-i18n'
import { describeApiError } from '../../../api/error-text'
import { StowBadge, StowButton, StowListItem } from '@/shared/ui'
import { useRetryUserHome, type AdminUser } from '../api'
import * as styles from './admin.css'

export function ManagedHome({ user, onSettings }: { user: AdminUser; onSettings: () => void }) {
  const { t } = useI18n()
  const retry = useRetryUserHome()
  if (!user.home?.enabled) return null
  return (
    <div>
      <StowListItem
        headline={
          <>
            <span>Home</span>
            <StowBadge>{t('user.home_managed')}</StowBadge>
          </>
        }
        supporting={user.home.ready ? t('user.home_auto_provided') : t('user.home_not_ready')}
        trailing={
          <>
            {!user.home.ready ? (
              <StowButton variant="outlined" loading={retry.isPending} onClick={() => retry.mutate(user.id)}>
                {t('user.retry_home')}
              </StowButton>
            ) : null}
            <StowButton variant="text" onClick={onSettings}>
              {t('server.home_folders')}
            </StowButton>
          </>
        }
      />
      {retry.error ? (
        <p role="alert" className={styles.sectionError}>
          {describeApiError(retry.error, t('user.could_not_prepare_home'))}
        </p>
      ) : null}
    </div>
  )
}
