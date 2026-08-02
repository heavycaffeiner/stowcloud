<script lang="ts">
  // SMB — DESIGN-AUTH.md §2.4/§10. The SMB password is the same as the
  // account password and is forced internal-network-only. Two independent
  // user toggles:
  //   smb_enabled  — the "publish" half. The other half (deployment-wide
  //                  smb.enabled) is an admin setting and can't be changed
  //                  here.
  //   smb_opt_out  — refuses NT hash derivation entirely. Turning it on
  //                  erases the stored hash immediately.
  import { t } from '../../i18n'
  import { api } from '../../api/client'
  import Switch from '../Switch.svelte'

  interface Props {
    optOut: boolean
    enabled: boolean
    onchanged?: () => void
  }
  let { optOut, enabled, onchanged }: Props = $props()

  let saving = $state(false)
  let error = $state<string | null>(null)

  async function apply(nextOptOut: boolean, nextEnabled: boolean): Promise<void> {
    saving = true
    error = null
    try {
      await api.updateSmbSettings(nextOptOut, nextEnabled)
      onchanged?.()
    } catch {
      error = t('smb.could_not_save_setting_try')
    } finally {
      saving = false
    }
  }

  function onToggleEnabled(checked: boolean): void {
    void apply(optOut, checked)
  }

  function onToggleOptOut(checked: boolean): void {
    // Opting out removes any stored credential server-side — publishing
    // makes no sense on top of that, so reflect it locally too.
    void apply(checked, checked ? false : enabled)
  }
</script>

<div class="sc-smb">
  <p class="sc-smb__note">
    {t('smb.smb_reachable_only_from_local')}
  </p>
  <div class="sc-smb__row">
    <Switch checked={enabled} label={t('smb.allow_smb_access')} onchange={onToggleEnabled} />
  </div>
  <div class="sc-smb__row">
    <Switch checked={optOut} label={t('smb.do_not_store_smb_credentials')} onchange={onToggleOptOut} />
  </div>
  {#if error}<p class="sc-smb__error" role="alert">{error}</p>{/if}
  {#if saving}<p class="sc-smb__saving">{t('common.saving')}</p>{/if}
</div>

<style>
  .sc-smb {
    display: flex;
    flex-direction: column;
    gap: 16px;
    max-width: 480px;
  }
  .sc-smb__note {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-smb__row {
    display: flex;
    align-items: center;
  }
  .sc-smb__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-smb__saving {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
</style>
