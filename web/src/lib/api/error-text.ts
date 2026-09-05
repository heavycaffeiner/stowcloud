// The one place a server-reported failure
// turns into a sentence.
//
// The server never sends prose meant for a screen. A refusal travels as the
// stable `code` of's envelope, plus (for the validation
// refusals that need to name a path or a field) a catalogue key and its
// placeholders in `detail.reason_key` / `detail.reason_params`
// (`AppError::invalid_keyed`). This module renders that into whatever
// language the reader picked.
//
// It deliberately never returns `err.message`. That was the previous
// behaviour in two places and it is what this module exists to remove: the
// settings screen printed `detail.reason` raw (Korean, whatever locale you
// were in) and the share screen reversed the server's English back into keys
// by substring, so a one-word copy edit on the server silently dropped it to
// a generic fallback.
import { ApiError } from './types'
import { t } from '../i18n'

/** Every `reason_key` the server can send, as literals so `i18n-check.mjs`
 *  sees them: they reach `t()` through a variable, and a key nothing names
 *  at a call site reads to that checker as dead copy. Doubling as an
 *  allowlist also makes this fail closed: an unrecognized key falls back to
 *  the caller's own message instead of rendering as a raw dotted string. */
const SERVER_KEYS = new Set<string>([
  /* i18n */ 'system.hardening_cannot_loosen',
  /* i18n */ 'settings.unknown_totp_policy',
  /* i18n */ 'settings.unknown_watch_backend',
  /* i18n */ 'settings.path_must_be_absolute',
  /* i18n */ 'settings.dir_does_not_exist',
  /* i18n */ 'settings.dir_not_writable',
  /* i18n */ 'settings.must_be_at_least_one',
  /* i18n */ 'settings.unknown_section',
  /* i18n */ 'settings.not_in_this_build',
  /* i18n */ 'settings.readonly_data_dir',
  /* i18n */ 'settings.readonly_smb_agent_socket',
  /* i18n */ 'settings.secret_is_write_only',
  /* i18n */ 'settings.invalid_cidr',
  /* i18n */ 'settings.invalid_bind_address',
  /* i18n */ 'settings.unknown_hardening_policy',
  /* i18n */ 'settings.guard_has_no_bound',
  /* i18n */ 'settings.required_when_enabled',
  /* i18n */ 'settings.issuer_must_be_https',
  /* i18n */ 'settings.bind_failed',
  /* i18n */ 'settings.must_be_a_string',
  /* i18n */ 'admin.share_rejected',
  /* i18n */ 'admin.chunk_below_floor',
  // Registering or editing a share, refused for something about the request
  // itself. Each names one field or one rule, because the add dialog has
  // seven fields and a bare "unprocessable" leaves an operator guessing
  // which of them the server meant.
  /* i18n */ 'admin.share_backend_unknown',
  /* i18n */ 'admin.share_backend_extra_config',
  /* i18n */ 'admin.share_backend_config_missing',
  /* i18n */ 'admin.share_backend_immutable',
  /* i18n */ 'admin.share_config_invalid',
  /* i18n */ 'admin.share_host_required',
  /* i18n */ 'admin.share_s3_fields_required',
  /* i18n */ 'admin.share_s3_secret_required',
  /* i18n */ 'admin.share_secret_not_clearable',
  /* i18n */ 'admin.share_vault_container_required',
  /* i18n */ 'admin.share_vault_password_required',
  /* i18n */ 'admin.share_vault_size_required',
  /* i18n */ 'admin.share_vault_size_unexpected',
  /* i18n */ 'admin.share_vault_create_immutable',
  // A share refused to change its encryption setting because it still holds
  // content: enabling or disabling both require an empty share, since this
  // server never holds a key that could read what is already stored.
  /* i18n */ 'encryption.share_not_empty',
  // A folder whose backing disk is not there. It names the share and the
  // reason, because "not found" for a path that is perfectly good sends a
  // person looking in the wrong place.
  /* i18n */ 'fs.share_broken',
  /* i18n */ 'settings.would_lock_you_out',
  /* i18n */ 'settings.proxy_range_is_everything',
  /* i18n */ 'settings.out_of_range',
  /* i18n */ 'settings.host_list_empty',
  /* i18n */ 'settings.invalid_host',
  /* i18n */ 'settings.host_role_conflict',
  /* i18n */ 'settings.duplicate_host',
  /* i18n */ 'settings.invalid_origin',
  /* i18n */ 'settings.canonical_url_not_an_app_host',
  /* i18n */ 'settings.path_is_not_a_directory',
  /* i18n */ 'settings.gid_zero_is_root',
  /* i18n */ 'settings.smb_render_failed',
  /* i18n */ 'settings.smb_config_dir_unavailable',
  /* i18n */ 'settings.above_kernel_watch_limit'
])

/** A wire `code` → catalogue key, and a `reason_key` → catalogue key for the
 *  refusals whose reason is narrower than their code.
 *
 *  Both are spelled exactly as `go/engine/http/apierr` sends them
 *  (`restTable` for the codes, `sentinels` for the reason keys). They drifted
 *  once already: this map still named `fs.precondition`, `quota.exceeded` and
 *  `share.broken`, none of which the server has ever sent, so a refused save
 *  or a full disk rendered as the caller's generic fallback and the editor's
 *  conflict branch never ran. A code with no entry is not a bug; the screen
 *  that made the request then says what it was doing, which is more use than
 *  a generic "an error occurred". */
const CODE_KEYS: Record<string, string> = {
  'fs.not_found': /* i18n */ 'error.fs_not_found',
  'fs.denied': /* i18n */ 'error.acl_denied',
  'fs.conflict': /* i18n */ 'error.fs_conflict',
  'fs.exists': /* i18n */ 'error.fs_conflict',
  'fs.not_empty': /* i18n */ 'error.fs_conflict',
  'fs.precondition_failed': /* i18n */ 'error.fs_precondition',
  'fs.locked': /* i18n */ 'error.fs_locked',
  'fs.no_space': /* i18n */ 'error.quota_exceeded',
  'fs.share_unavailable': /* i18n */ 'error.share_broken',
  'link.expired': /* i18n */ 'error.fs_gone',
  'internal': /* i18n */ 'error.internal',
  'not_implemented': /* i18n */ 'error.not_implemented'
}

/** The reason keys worth naming on their own. `fs.invalid_name` arrives under
 *  the generic `unprocessable` code, and a rejected name is the one refusal a
 *  rename dialog can act on. */
const REASON_KEYS: Record<string, string> = {
  'fs.invalid_name': /* i18n */ 'error.fs_invalid_name',
  'fs.quota_exceeded': /* i18n */ 'error.quota_exceeded',
  'dav.locked': /* i18n */ 'error.fs_locked'
}

function params(detail: Record<string, unknown> | undefined): Record<string, string> | undefined {
  const p = detail?.reason_params
  return p && typeof p === 'object' ? (p as Record<string, string>) : undefined
}

/** The catalogue key for one wire error, or `null` if it carries neither a
 *  known `reason_key` nor a known `code`; the caller then says what *it*
 *  was trying to do, which is more use than a generic "an error occurred". */
function keyFor(code: string, detail: Record<string, unknown> | undefined): string | null {
  const reasonKey = detail?.reason_key
  if (typeof reasonKey === 'string') {
    if (SERVER_KEYS.has(reasonKey)) return reasonKey
    if (REASON_KEYS[reasonKey]) return REASON_KEYS[reasonKey]
  }
  return CODE_KEYS[code] ?? null
}

/** Renders an `ApiError` in the reader's language. `fallback` is already-
 *  translated text naming the action that failed ("Could not save the SMB
 *  settings"), used whenever the server's own answer isn't one this build
 *  knows. The caller resolves it with its own `t()` so the checker can see
 *  that key at a call site. */
export function describeApiError(err: unknown, fallback: string): string {
  if (!(err instanceof ApiError)) return fallback
  const key = keyFor(err.code, err.detail)
  return key ? t(key, params(err.detail)) : fallback
}

/** A `SettingsField.readonly_reason_key` from the settings snapshot. An
 *  unknown key renders as itself, visibly odd, which is the right signal for
 *  a client older than the server it is talking to, and better than leaving a
 *  field the admin cannot edit with no explanation next to it. */
export function serverKeyText(key: string): string {
  return SERVER_KEYS.has(key) ? t(key) : key
}

/** Same, for a per-item batch/job failure: the `{code, message, detail}`
 *  object `OpResult.error` carries (`go/internal/httpapi/handler/ops.go` renders it
 *  with the identical `AppError::to_wire` the envelope uses). Returns a
 *  catalogue key and its placeholders rather than text, because the job tray
 *  stores this across a render boundary and resolves it at paint time. */
export function batchErrorKey(
  error: { code: string; detail?: Record<string, unknown> } | undefined
): { key: string; params?: Record<string, string> } | null {
  if (!error) return null
  const key = keyFor(error.code, error.detail)
  return key ? { key, params: params(error.detail) } : null
}
