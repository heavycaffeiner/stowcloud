// web/src/lib/api/error-text.ts — the one place a server-reported failure
// turns into a sentence.
//
// The server never sends prose meant for a screen. A refusal travels as the
// stable `code` of's envelope, plus — for the validation
// refusals that need to name a path or a field — a catalogue key and its
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
 *  sees them — they reach `t()` through a variable, and a key nothing names
 *  at a call site reads to that checker as dead copy. Doubling as an
 *  allowlist also makes this fail closed: an unrecognized key falls back to
 *  the caller's own message instead of rendering as a raw dotted string. */
const SERVER_KEYS = new Set<string>([
  /* i18n */ 'settings.unknown_totp_policy',
  /* i18n */ 'settings.unknown_symlink_policy',
  /* i18n */ 'settings.unknown_oidc_smb_policy',
  /* i18n */ 'settings.unknown_watch_backend',
  /* i18n */ 'settings.path_must_be_absolute',
  /* i18n */ 'settings.dir_does_not_exist',
  /* i18n */ 'settings.dir_not_writable',
  /* i18n */ 'settings.data_dir_missing_auth_db',
  /* i18n */ 'settings.master_key_current_unreadable',
  /* i18n */ 'settings.master_key_target_unreadable',
  /* i18n */ 'settings.master_key_contents_differ',
  /* i18n */ 'settings.invalid_bind_address',
  /* i18n */ 'settings.must_be_at_least_one',
  /* i18n */ 'settings.invalid_origin',
  /* i18n */ 'settings.oidc_redirect_uri_must_be_https',
  /* i18n */ 'settings.oidc_redirect_host_not_served',
  /* i18n */ 'settings.oidc_secret_file_missing',
  /* i18n */ 'settings.unknown_section',
  /* i18n */ 'settings.stale_client',
  /* i18n */ 'settings.not_in_this_build',
  /* i18n */ 'settings.readonly_secret_file_path',
  /* i18n */ 'settings.readonly_local_password_login',
  /* i18n */ 'settings.readonly_owned_by_index_section',
  /* i18n */ 'settings.readonly_owned_by_upload_section',
  /* i18n */ 'settings.readonly_smb_agent_socket',
  /* i18n */ 'settings.readonly_smb_interfaces',
  /* i18n */ 'settings.readonly_static_smb_shares',
  /* i18n */ 'settings.readonly_static_bootstrap_shares',
  /* i18n */ 'share.path_does_not_exist',
  /* i18n */ 'share.path_permission_denied',
  /* i18n */ 'share.path_unreadable',
  /* i18n */ 'share.path_not_a_directory',
  /* i18n */ 'share.name_empty',
  /* i18n */ 'share.name_taken',
  /* i18n */ 'share.path_overlaps',
  /* i18n */ 'share.config_defined_not_deletable'
])

/** `ErrorCode::as_str` (`crates/sc-http/src/error.rs`) → catalogue key. Only
 *  the codes a per-item batch failure can carry are here; a whole-request
 *  failure is reported by the screen that made the request, in its own
 *  words. `internal` has no dot and so cannot be a catalogue key itself,
 *  which is the other reason this mapping is explicit rather than derived. */
const CODE_KEYS: Record<string, string> = {
  'fs.not_found': /* i18n */ 'error.fs_not_found',
  'fs.conflict': /* i18n */ 'error.fs_conflict',
  'fs.precondition': /* i18n */ 'error.fs_precondition',
  'fs.invalid_name': /* i18n */ 'error.fs_invalid_name',
  'fs.gone': /* i18n */ 'error.fs_gone',
  'acl.denied': /* i18n */ 'error.acl_denied',
  'quota.exceeded': /* i18n */ 'error.quota_exceeded',
  'internal': /* i18n */ 'error.internal',
  'internal.not_implemented': /* i18n */ 'error.not_implemented'
}

function params(detail: Record<string, unknown> | undefined): Record<string, string> | undefined {
  const p = detail?.reason_params
  return p && typeof p === 'object' ? (p as Record<string, string>) : undefined
}

/** The catalogue key for one wire error, or `null` if it carries neither a
 *  known `reason_key` nor a known `code` — the caller then says what *it*
 *  was trying to do, which is more use than a generic "an error occurred". */
function keyFor(code: string, detail: Record<string, unknown> | undefined): string | null {
  const reasonKey = detail?.reason_key
  if (typeof reasonKey === 'string' && SERVER_KEYS.has(reasonKey)) return reasonKey
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
 *  unknown key renders as itself — visibly odd, which is the right signal for
 *  a client older than the server it is talking to, and better than leaving a
 *  field the admin cannot edit with no explanation next to it. */
export function serverKeyText(key: string): string {
  return SERVER_KEYS.has(key) ? t(key) : key
}

/** Same, for a per-item batch/job failure — the `{code, message, detail}`
 *  object `OpResult.error` carries (`bridge.rs::http_op_result` renders it
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
