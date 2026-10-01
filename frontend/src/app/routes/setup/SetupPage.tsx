import { Link } from 'react-router-dom'
import { scorePasswordStrength } from '../../../lib/format/password-strength'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { PathPickerDialog } from '../../../features/files/PathPickerDialog'
import { TextField } from '../../../ui/TextField'
import { useDocumentTitle } from '../../hooks/use-document-title'
import { useSetupFlows, MIN_PASSWORD_LENGTH, toList } from './hooks/use-setup-flows'
import { useSetupState } from './hooks/use-setup-state'
import type { SetupFinding } from '../../../lib/api/setup'
import { ProgressLinear } from '../../../ui/ProgressLinear'
import * as authStyles from '../auth.css'
import * as utilitiesStyles from '../../../ui/utilities.css'
import { cx } from '../../../ui/cx'

function warningText(
  t: (key: string, params?: Record<string, string | number>) => string,
  finding: SetupFinding
): string {
  return t(finding.reason, finding.args ?? {})
}

export function SetupPage() {
  const { t } = useI18n()
  const { state, actions } = useSetupState()
  const flows = useSetupFlows(state, actions)
  const {
    token,
    username,
    password,
    passwordConfirm,
    appHosts,
    trustedProxies,
    shareName,
    sharePath,
    accountCreated,
    doneButLoginFailed,
    shareFailed,
    shareRetryError,
    errorMessage,
    warnings,
    pickerOpen,
    pickerAuthenticated,
    step
  } = state
  const strength = scorePasswordStrength(password)
  const passwordError =
    password && password.length < MIN_PASSWORD_LENGTH
      ? t('setup.password_must_at_least_characters', { min: MIN_PASSWORD_LENGTH })
      : null
  const confirmError = passwordConfirm && passwordConfirm !== password ? t('setup.passwords_do_not_match') : null
  const canSubmit =
    !flows.setup.isPending &&
    !flows.login.isPending &&
    !flows.retryShare.isPending &&
    token.trim().length > 0 &&
    username.trim().length > 0 &&
    password.length >= MIN_PASSWORD_LENGTH &&
    passwordConfirm === password &&
    toList(appHosts).length > 0 &&
    (shareName.trim() === '') === (sharePath.trim() === '')
  useDocumentTitle(t('setup.create_administrator_account'))
  return (
    <main className={authStyles.page}>
      <form className={authStyles.card} onSubmit={flows.submit}>
        <h1 className={authStyles.title}>{t('setup.create_administrator_account')}</h1>
        <p className={authStyles.subtitle}>
          {t('setup.on_server_s_first_start')} <code>setup-token</code> {t('setup.file_data_directory')}
        </p>
        {accountCreated ? (
          <>
            <p className={authStyles.success} role="status">
              {t('setup.account_created')}
            </p>
            {doneButLoginFailed ? (
              <p className={authStyles.error} role="alert">
                <Link to="/login">{t('setup.go_sign_page')}</Link> {t('setup.sign')}
              </p>
            ) : null}
            {warnings.length > 0 ? (
              <div className={authStyles.warning} role="status">
                {warnings.map((warning, index) => (
                  <p key={`${warning.reason}-${index}`} className={authStyles.warningText}>
                    {warningText(t, warning)}
                  </p>
                ))}
              </div>
            ) : null}
            {shareFailed ? (
              <>
                <p className={authStyles.error} role="alert">
                  {t('setup.account_created_share_failed')}
                </p>
                <TextField
                  value={shareName}
                  label={t('common.name')}
                  autoComplete="off"
                  onValueChange={actions.setShareName}
                />
                <div className={authStyles.pathRow}>
                  <TextField
                    className={authStyles.pathField}
                    value={sharePath}
                    label={t('folder_share.server_path')}
                    autoComplete="off"
                    onValueChange={actions.setSharePath}
                  />
                  <Button
                    className={authStyles.pathButton}
                    variant="outlined"
                    disabled={!pickerAuthenticated}
                    onClick={() => actions.patch({ pickerOpen: true })}
                  >
                    {t('picker.browse_folder')}
                  </Button>
                </div>
                {shareRetryError ? (
                  <p className={authStyles.error} role="alert">
                    {shareRetryError}
                  </p>
                ) : null}
                {shareRetryError && pickerAuthenticated ? (
                  <Link className={cx(authStyles.setupLink, utilitiesStyles.focusRing)} to="/admin#shares">
                    {t('setup.go_to_admin_shares')}
                  </Link>
                ) : null}
                <div className={authStyles.actions}>
                  <Button
                    className={authStyles.action}
                    disabled={
                      flows.login.isPending ||
                      flows.retryShare.isPending ||
                      shareName.trim().length === 0 ||
                      sharePath.trim().length === 0
                    }
                    loading={flows.login.isPending || flows.retryShare.isPending}
                    onClick={() => void flows.continueAfterSetup()}
                  >
                    {t('setup.retry_first_share')}
                  </Button>
                </div>
              </>
            ) : (
              <div className={authStyles.actions}>
                <Button
                  className={authStyles.action}
                  loading={flows.login.isPending}
                  onClick={() => void flows.continueAfterSetup()}
                >
                  {t('setup.continue_anyway')}
                </Button>
              </div>
            )}
          </>
        ) : (
          <>
            <ol className={authStyles.steps} aria-label={t('progress.progress')}>
              {[
                t('setup.create_administrator_account'),
                t('setup.how_this_server_is_reached'),
                t('setup.first_shared_folder')
              ].map((label, index) => (
                <li
                  key={label}
                  className={cx(authStyles.step, step === index + 1 && authStyles.stepActive)}
                  aria-current={step === index + 1 ? 'step' : undefined}
                >
                  {index + 1}. {label}
                </li>
              ))}
            </ol>
            {step === 1 ? (
              <>
                <TextField
                  value={token}
                  label={t('setup.setup_token')}
                  autoFocus
                  autoComplete="off"
                  onValueChange={actions.setToken}
                />
                <TextField
                  value={username}
                  label={t('setup.administrator_username')}
                  autoComplete="username"
                  onValueChange={actions.setUsername}
                />
                <TextField
                  value={password}
                  label={t('common.password')}
                  type="password"
                  error={passwordError}
                  autoComplete="new-password"
                  onValueChange={actions.setPassword}
                />
                {password ? (
                  <div className={authStyles.strength}>
                    <ProgressLinear
                      className={authStyles.strengthBar}
                      value={strength.ratio}
                      tone={strength.tier}
                      label={t('common.password_strength', { level: strength.label })}
                    />
                    <span className={authStyles.strengthLabel}>{strength.label}</span>
                  </div>
                ) : null}
                <TextField
                  value={passwordConfirm}
                  label={t('setup.confirm_password')}
                  type="password"
                  error={confirmError}
                  autoComplete="new-password"
                  onValueChange={actions.setPasswordConfirm}
                />
              </>
            ) : null}
            {step === 2 ? (
              <>
                <TextField
                  value={appHosts}
                  label={t('server.app_hosts_comma_separated')}
                  autoComplete="off"
                  onValueChange={actions.setAppHosts}
                />
                <p className={authStyles.hint}>{t('setup.app_hosts_hint')}</p>
                <TextField
                  value={trustedProxies}
                  label={t('server.trusted_proxies_comma_separated')}
                  autoComplete="off"
                  onValueChange={actions.setTrustedProxies}
                />
                <p className={authStyles.hint}>{t('setup.trusted_proxies_hint')}</p>
              </>
            ) : null}
            {step === 3 ? (
              <>
                <p className={authStyles.hint}>{t('setup.first_share_hint')}</p>
                <TextField
                  value={shareName}
                  label={t('common.name')}
                  autoComplete="off"
                  onValueChange={actions.setShareName}
                />
                <div className={authStyles.pathRow}>
                  <TextField
                    className={authStyles.pathField}
                    value={sharePath}
                    label={t('folder_share.server_path')}
                    autoComplete="off"
                    onValueChange={actions.setSharePath}
                  />
                  <Button
                    className={authStyles.pathButton}
                    variant="outlined"
                    onClick={() => actions.patch({ pickerOpen: true })}
                  >
                    {t('picker.browse_folder')}
                  </Button>
                </div>
              </>
            ) : null}
            {warnings.length > 0 ? (
              <div className={authStyles.warning} role="alert">
                {warnings.map((warning, index) => (
                  <p key={`${warning.reason}-${index}`} className={authStyles.warningText}>
                    {warningText(t, warning)}
                  </p>
                ))}
              </div>
            ) : null}
            {errorMessage ? (
              <p className={authStyles.error} role="alert">
                {errorMessage}
              </p>
            ) : null}
            <div className={authStyles.actions}>
              {step > 1 ? (
                <Button className={authStyles.action} variant="outlined" type="button" onClick={actions.previousStep}>
                  {t('common.back')}
                </Button>
              ) : null}
              {step < 3 ? (
                <Button className={authStyles.action} type="button" onClick={actions.nextStep}>
                  {t('common.continue')}
                </Button>
              ) : (
                <Button
                  className={authStyles.action}
                  type="submit"
                  disabled={!canSubmit}
                  loading={flows.setup.isPending || flows.login.isPending}
                >
                  {t('setup.create_administrator_account')}
                </Button>
              )}
            </div>
            <Link className={cx(authStyles.setupLink, utilitiesStyles.focusRing)} to="/login">
              {t('setup.already_have_account_sign')}
            </Link>
          </>
        )}
      </form>
      <PathPickerDialog
        open={pickerOpen}
        mode="folder"
        start={sharePath}
        token={pickerAuthenticated ? undefined : token}
        onClose={() => actions.patch({ pickerOpen: false })}
        onPick={(path) => actions.patch({ sharePath: path, pickerOpen: false })}
      />
    </main>
  )
}
