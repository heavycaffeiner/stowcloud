package fromrust

// Every statement, as a constant. The selects name their columns rather than
// taking a star, so a column added to the old schema after this was written
// does not silently shift the one beside it.
const (
	driverName = "sqlite"

	// query_only refuses a write on the connection itself, so a mistake here
	// cannot reach a file the operator still has to be able to roll back to.
	// busy_timeout because the old server may still be shutting down.
	queryOnlyDSN = "?_pragma=busy_timeout(5000)&_pragma=query_only(true)"

	// The old grant table's principal kinds.
	principalUser  = 0
	principalGroup = 1
)

const (
	selUser = `
SELECT id, name, display, pw_hash, disabled, role, quota_bytes, usage_bytes,
       smb_opt_out, smb_enabled, created_ns
FROM user`
	insUser = `
INSERT INTO user(id, name, display, pw_hash, disabled, role, quota_bytes, usage_bytes,
                 smb_opt_out, smb_enabled, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	selSMB = `SELECT user, nt_hash_ct, key_ver FROM user_smb_secret`
	insSMB = `INSERT INTO user_smb_secret(user, nt_hash_ct, key_ver) VALUES (?, ?, ?)`

	// time_step is the Rust spelling of the replay step, and used_ns is
	// stamped at import because the old table kept no timestamp.
	selTotpUsed = `SELECT user, time_step FROM totp_used`
	insTotpUsed = `INSERT INTO totp_used(user, step, used_ns) VALUES (?, ?, ?)`

	sqlHasTable = `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`

	selKeyVersion = `SELECT ver FROM key_version WHERE id = 1`

	//nolint:gosec // G101 reads the identifier: this one names a table, and the value is a statement.
	selTotpSecret = `SELECT id, totp_secret, created_ns FROM user WHERE totp_secret IS NOT NULL`
	//nolint:gosec // as above.
	insTotpSecret = `INSERT INTO totp_secret(user, secret_ct, key_ver, created_ns) VALUES (?, ?, ?, ?)`

	selGroup = `SELECT id, name FROM group_`
	insGroup = `INSERT INTO "group"(id, name) VALUES (?, ?)`

	selMembership = `SELECT user, group_ FROM membership`
	insMembership = `INSERT INTO membership(user, "group") VALUES (?, ?)`

	selSession = `
SELECT id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr
FROM session`
	insSession = `
INSERT INTO session(id_hash, user, created_ns, last_seen_ns, absolute_expiry_ns, ip_first, ua_first, amr)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	//nolint:gosec // as above.
	selAppPassword = `
SELECT id, token_hash, user, name, scope_perms, scope_shares, created_ns,
       last_used_ns, last_ip, last_ua, expires_ns, wipe_requested
FROM app_password`
	//nolint:gosec // as above.
	insAppPassword = `
INSERT INTO app_password(id, token_hash, user, name, scope_perms, scope_shares, created_ns,
                         last_used_ns, last_ip, last_ua, expires_ns, wipe_requested)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	selRecoveryCode = `SELECT user, code_hash, used_ns FROM recovery_code`
	insRecoveryCode = `INSERT INTO recovery_code(user, code_hash, used_ns) VALUES (?, ?, ?)`

	selOidcLink = `SELECT issuer, subject, user, linked_ns, last_login_ns FROM oidc_identity`
	insOidcLink = `
INSERT INTO oidc_link(issuer, subject, user, linked_ns, last_login_ns) VALUES (?, ?, ?, ?, ?)`

	selAudit = `SELECT ts_ns, actor, event, target, ip, ua, result, detail FROM audit`
	insAudit = `
INSERT INTO audit(ts_ns, actor, event, target, ip, ua, result, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	selGrant = `
SELECT id, principal_kind, principal_id, share, subpath, allow, deny, inherit, label, created_ns
FROM grant_`
	insGrant = `
INSERT INTO "grant"(id, user, "group", share, subpath, allow, deny, inherit, label, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	selShareLink = `
SELECT id, token_hash, token_enc, share, path, fileid, owner, perms, password_hash,
       expires_ns, max_downloads, downloads, label, note, created_ns
FROM share_link`
	insShareLink = `
INSERT INTO share_link(id, token_hash, token_enc, token_key_ver, share, path,
                       dev, ino, btime_present, btime_ns,
                       owner, perms, password_hash, expires_ns, max_downloads, downloads,
                       label, note, created_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// expires_ns is TEXT in the old schema and INTEGER here, so it is parsed
	// rather than carried.
	selDavLock = `
SELECT token, fileid, share, path, principal, owner, depth, scope, expires_ns, timeout_s
FROM dav_lock`
	insDavLock = `
INSERT INTO dav_lock(token, share, dev, ino, btime_present, btime_ns,
                     path, principal, owner, depth, scope, expires_ns, timeout_s)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	selSettings = `SELECT id, json FROM settings_overrides`
	insSettings = `INSERT INTO settings(id, json) VALUES (?, ?)`

	// The administrator's index override. Presence of the row is the override:
	// absence has to stay absence so the configured default still wins.
	selIndexSettings = `SELECT name_enabled FROM index_settings WHERE id = 1`

	// ncInstanceIDKey is the key the old key-value table stores the identity
	// under.
	ncInstanceIDKey = "instanceid"

	// The compat layer's durable rows.
	// The old table is a key-value pair, not a single-row record, and the id
	// lives under one known key.
	selNcInstance = `SELECT v FROM nc_instance WHERE k = ?`
	insCompatKV   = `INSERT INTO compat_kv(key, value) VALUES (?, ?)
	                 ON CONFLICT(key) DO UPDATE SET value = excluded.value`

	// The alias rows, which name an in-flight upload by a client-chosen id.
	selNcUploadAlias     = `SELECT user, tid, session FROM nc_upload_alias`
	insCompatUploadAlias = `INSERT INTO compat_upload_alias(user, tid, session) VALUES (?, ?, ?)`

	// The login flows. Only the digests and the approval marker come across:
	// the plaintext column the old table carried is deliberately not read.
	// The old table keys a flow by two digests and records the approval as a
	// login name rather than a user id, so the importer resolves the name to
	// an account rather than reading an id that is not there.
	selNcLoginFlow = `SELECT poll_hash, flow_hash, created_ns, login_name, app_password
	                    FROM nc_login_flow`
	insNcLoginFlow = `INSERT INTO compat_login_flow(poll_digest, login_digest, created_ns,
	                                                approved_user, approved_login, last_poll_ns)
	                  VALUES (?, ?, ?, ?, ?, 0)`

	// The credential an approved flow already minted, revoked before the flow
	// is translated so no orphan is left behind.
	//nolint:gosec // G101: a statement naming a column, not a credential.
	delAppPasswordByHash = `DELETE FROM app_password WHERE token_hash = ?`

	// The settings row this import may have just written, read back so the
	// index override merges into it rather than replacing it.
	selSettingsRow = `SELECT json FROM settings WHERE id = 1`
	upsSettingsRow = `INSERT INTO settings(id, json) VALUES (1, ?)
	                  ON CONFLICT(id) DO UPDATE SET json = excluded.json`

	// The interval blob is selected last, because the destination holds it as
	// a table of its own rather than as a column. spooled_names is coalesced
	// because a zero-length blob comes back from the driver as nothing at all,
	// and the column it lands in is NOT NULL with that same empty default.
	selUploadSession = `
SELECT id, user, share, dest, part_name, spool_dir, mode, total_len, chunk_size, random_access,
       next_name, write_head, spooled_names, if_match, filename, mtime_ns, mime, relative_path,
       verify, verify_digest, created_ns, expires_ns, state, received
FROM upload_sessions`
	insUploadSession = `
INSERT INTO upload_session(id, user, share, dest, part_name, spool_dir, mode, total_len,
                           chunk_size, random_access, next_name, write_head, spooled_names,
                           if_match, filename, mtime_ns, mime, relative_path, verify,
                           verify_digest, created_ns, expires_ns, state)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, coalesce(?, X''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	insUploadInterval = `INSERT INTO upload_interval(session, lo, hi) VALUES (?, ?, ?)`

	// An alias is what makes a named chunk collection resumable after the
	// cutover: without it the transfer id the client is still using means
	// nothing to the new binary, and an upload part way through a large file
	// starts again.
	selUploadAlias = `SELECT tid, user, session, share, dest, created_ns FROM upload_alias`
	insUploadAlias = `
INSERT INTO upload_alias(tid, user, session, share, dest, created_ns)
VALUES (?, ?, ?, ?, ?, ?)`

	// The chunk floor and default an admin stored. The row's presence is the
	// fact being carried, not just the numbers: it is what tells the settings
	// screen an admin chose these rather than the config file supplying them.
	selUploadChunkSettings = `SELECT chunk_min, chunk_default FROM upload_chunk_settings WHERE id = 1`
	insUploadChunkSettings = `
INSERT INTO upload_chunk_settings(id, chunk_min, chunk_default) VALUES (1, ?, ?)`

	// The directories part files were created in. They come across so the
	// first sweep after the cutover can still find an orphan the old build
	// left behind.
	selUploadTouchedDirs = `SELECT share, dir FROM upload_touched_dirs`
	insUploadTouchedDir  = `INSERT INTO upload_touched_dir(share, dir) VALUES (?, ?)`

	selNodeIdent = `SELECT share, dev, ino, btime_ns FROM node WHERE id = ?`

	selDavProp = `SELECT fileid, ns, name, value FROM dav_prop`
	insDavProp = `
INSERT INTO dav_prop(share, dev, ino, btime_present, btime_ns, ns, name, value)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	selFavorite = `SELECT user, fileid FROM nc_favorite`
	insFavorite = `
INSERT INTO favorite(user, share, dev, ino, btime_present, btime_ns) VALUES (?, ?, ?, ?, ?, ?)`

	// The Phase 4 shares.db and jobs.db. The Rust share_ table's rowid is what
	// DYNAMIC_SHARE_ID_BASE was added to, so the id is carried as-is: grants
	// and API payloads already refer to the external id, and the destination
	// keeps the same arithmetic.
	selShare = `SELECT id, name, host_path, created_ns FROM share_`
	insShare = `
INSERT INTO share_definition(id, name, host_path, created_ns) VALUES (?, ?, ?, ?)`

	selShareIdentityOverride = `SELECT share_id, name, host_path FROM share_identity_override`
	insShareIdentityOverride = `
INSERT INTO share_identity_override(share_id, name, host_path) VALUES (?, ?, ?)
ON CONFLICT(share_id) DO UPDATE SET name = excluded.name, host_path = excluded.host_path`

	selShareTrashOverride = `SELECT share_id, enabled FROM share_trash_override`
	insShareTrashOverride = `
INSERT INTO share_trash_override(share_id, enabled) VALUES (?, ?)
ON CONFLICT(share_id) DO UPDATE SET enabled = excluded.enabled`

	selJob = `
SELECT id, owner, kind, state, done, total, current, created_at, updated_at
FROM jobs`
	insJob = `
INSERT INTO operation(id, user, kind, state, progress, total, message, cancellation, created_ns, finished_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`

	selJobResult = `SELECT job_id, seq, path, status, error, will_copy FROM job_results`
	insJobResult = `
INSERT INTO operation_result(operation, idx, path, ok, reason, text)
VALUES (?, ?, ?, ?, ?, ?)`
)

// selUserTables is a database's own account of what it holds. sqlite_% is
// SQLite's own bookkeeping and is never a source table.
const selUserTables = `
SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
