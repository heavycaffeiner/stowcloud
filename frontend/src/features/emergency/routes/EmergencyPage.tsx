import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useForm, useWatch } from 'react-hook-form'
import { emergencyDoor, emergencyLogin, emergencySettings, type EmergencySettings } from '../api'
import { describeApiError } from '../../../api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { StowButton, StowFormTextField } from '@/shared/ui'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { EmergencyEditor } from '../components/EmergencyEditor'
import * as styles from './EmergencyPage.css'

export function EmergencyPage() {
  const { t } = useI18n()
  useDocumentTitle(t('emergency.emergency_settings'))
  const door = useQuery({
    queryKey: ['emergency', 'door'],
    queryFn: emergencyDoor,
    retry: false,
    refetchOnWindowFocus: false
  })
  // Signing in hands over the settings the editor opens with.
  const [settings, setSettings] = useState<EmergencySettings | null>(null)

  return (
    <main className={styles.root}>
      <div className={styles.card}>
        <h1 className={styles.title}>{t('emergency.emergency_settings')}</h1>
        <p className={styles.hint}>{t('emergency.subtitle')}</p>
        {door.data?.reason ? (
          <p className={styles.warning} role="alert">
            {t('emergency.the_server_is_degraded', { reason: door.data.reason })}
          </p>
        ) : null}
        {door.error ? (
          <p className={styles.error} role="alert">
            {describeApiError(door.error, t('emergency.something_went_wrong'))}
          </p>
        ) : null}
        {door.isPending ? (
          <p className={styles.hint}>{t('common.loading')}</p>
        ) : door.data?.setup_required ? (
          <>
            <p className={styles.hint}>{t('emergency.no_administrator_yet')}</p>
            <div className={styles.actions}>
              <StowButton
                onClick={() => {
                  window.location.href = '/setup'
                }}
              >
                {t('emergency.go_to_setup')}
              </StowButton>
            </div>
          </>
        ) : settings ? (
          <EmergencyEditor initial={settings} />
        ) : (
          <EmergencySignIn onSignedIn={setSettings} />
        )}
      </div>
    </main>
  )
}

interface Credentials {
  username: string
  password: string
  code: string
}

function EmergencySignIn({ onSignedIn }: { onSignedIn: (settings: EmergencySettings) => void }) {
  const { t } = useI18n()
  const { control, handleSubmit, setError, formState } = useForm<Credentials>({
    defaultValues: { username: '', password: '', code: '' }
  })
  const [username, password, code] = useWatch({ control, name: ['username', 'password', 'code'] })
  // The password was right and the account still owes a second factor.
  const [needsCode, setNeedsCode] = useState(false)
  const signIn = useMutation({
    mutationFn: async ({ username, password, code }: Credentials): Promise<EmergencySettings | null> => {
      const result = await emergencyLogin(username.trim(), password, needsCode ? code : undefined)
      return result.status === 'totp_required' ? null : emergencySettings()
    }
  })
  const ready = (values: Credentials): boolean =>
    values.username.trim() !== '' && values.password !== '' && (!needsCode || values.code.trim() !== '')

  // Enter in a field submits even while the button is busy.
  const submit = handleSubmit(async (values) => {
    if (signIn.isPending || !ready(values)) return
    try {
      const settings = await signIn.mutateAsync(values)
      if (settings) onSignedIn(settings)
      else setNeedsCode(true)
    } catch (error) {
      setError('root', { message: describeApiError(error, t('emergency.something_went_wrong')) })
    }
  })

  return (
    <>
      {formState.errors.root?.message ? (
        <p className={styles.error} role="alert">
          {formState.errors.root.message}
        </p>
      ) : null}
      <form className={styles.form} onSubmit={(event) => void submit(event)}>
        {needsCode ? (
          <>
            <p className={styles.hint}>{t('emergency.enter_your_code')}</p>
            <StowFormTextField
              control={control}
              name="code"
              label={t('login.verification_code')}
              autoFocus
              autoComplete="one-time-code"
            />
          </>
        ) : (
          <>
            <StowFormTextField
              control={control}
              name="username"
              label={t('login.username')}
              autoFocus
              autoComplete="username"
            />
            <StowFormTextField
              control={control}
              name="password"
              label={t('common.password')}
              type="password"
              autoComplete="current-password"
            />
          </>
        )}
        <div className={styles.actions}>
          <StowButton type="submit" loading={signIn.isPending} disabled={!ready({ username, password, code })}>
            {t('login.sign')}
          </StowButton>
        </div>
      </form>
    </>
  )
}
