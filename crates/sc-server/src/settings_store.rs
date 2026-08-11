//! Persisted admin overrides for the server-settings screen (`settings.db`).
//!
//! Same shape as `sc-search`'s `IndexSettingsStore` and `sc-upload`'s
//! `upload_chunk_settings`: a single row, `CHECK (id = 1)`, absence meaning
//! "no override yet". This one stores the whole [`crate::config::
//! SettingsOverrides`] as one JSON blob rather than one column per field —
//! unlike those two precedents, this store's shape grows with every config
//! section the settings screen covers, and a JSON blob is the one encoding
//! that doesn't need a schema migration each time a field is added to it.
//! It deliberately does **not** rewrite `config.toml`. The reason is not the
//! one this comment used to give: `scripts/deploy.sh` pushes a binary and a
//! build context, and the Dockerfile copies `Cargo.toml`, `crates/`, `web/`
//! and two licence files, so nothing in a rollout touches `sc.toml` at all.
//! The reason that does hold is that the file is the operator's: a server
//! that rewrote it would have to merge with whatever they were editing, and a
//! comment-preserving TOML round-trip is a dependency and a class of bug this
//! does not need. What makes the split safe is that an override is visible
//! and reversible (`DELETE /api/admin/server-settings/{section}`), not that
//! it is authoritative.

use std::path::Path;

use parking_lot::Mutex;
use rusqlite::{Connection, OptionalExtension};

use crate::config::SettingsOverrides;

const SCHEMA: &str = "\
    CREATE TABLE IF NOT EXISTS settings_overrides (\
        id       INTEGER PRIMARY KEY CHECK (id = 1),\
        json     TEXT NOT NULL\
    );";

pub struct SettingsStore {
    db: Mutex<Connection>,
    /// Live cached value — every reader (snapshot, patch handlers) reads
    /// this instead of re-querying SQLite per call.
    cached: Mutex<SettingsOverrides>,
}

impl SettingsStore {
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::from_conn(Connection::open(path)?)
    }

    #[cfg(test)]
    pub fn open_in_memory() -> anyhow::Result<Self> {
        Self::from_conn(Connection::open_in_memory()?)
    }

    fn from_conn(conn: Connection) -> anyhow::Result<Self> {
        conn.pragma_update(None, "journal_mode", "WAL")?;
        conn.execute_batch("PRAGMA synchronous = NORMAL; PRAGMA busy_timeout = 5000;")?;
        conn.execute_batch(SCHEMA)?;
        let stored: Option<String> = conn
            .query_row(
                "SELECT json FROM settings_overrides WHERE id = 1",
                [],
                |r| r.get(0),
            )
            .optional()?;
        let overrides = match stored {
            Some(json) => {
                let migrated = Self::migrate_renamed_keys(&json);
                if let Some(next) = &migrated {
                    conn.execute(
                        "UPDATE settings_overrides SET json = ?1 WHERE id = 1",
                        rusqlite::params![next],
                    )?;
                }
                serde_json::from_str(migrated.as_deref().unwrap_or(&json)).unwrap_or_default()
            }
            None => SettingsOverrides::default(),
        };
        Ok(Self {
            db: Mutex::new(conn),
            cached: Mutex::new(overrides),
        })
    }

    /// Rewrite a row written before `public_origins`/`redirect_uris` existed,
    /// returning the new JSON when anything changed.
    ///
    /// This is the one place an operator cannot edit by hand, which is why it
    /// gets a migration where `config.toml` gets a refusal. The failure it
    /// prevents is worse than a rename: the fields carry no serde default, so
    /// a stale row fails to deserialize, and `from_conn` reads it with
    /// `unwrap_or_default()` — silently discarding *every* override an
    /// administrator has ever made, across all ten sections. That is exactly
    /// what `SettingsOverrides`'s container-level `#[serde(default)]` exists
    /// to prevent, and a field rename defeats it.
    ///
    /// Idempotent: it only acts when the old key is present and the new one
    /// is not, so a second run leaves the row alone.
    fn migrate_renamed_keys(json: &str) -> Option<String> {
        let mut doc: serde_json::Value = serde_json::from_str(json).ok()?;
        let mut changed = false;

        if let Some(net) = doc.get_mut("network").and_then(|v| v.as_object_mut()) {
            if !net.contains_key("public_origins") {
                // A stored `null` means the administrator cleared that box,
                // so it becomes `[]` — "no declared origin" — rather than a
                // one-element list holding nothing.
                if let Some(old) = net.remove("compat_canonical_url") {
                    let moved = match old {
                        serde_json::Value::String(s) if !s.trim().is_empty() => {
                            vec![serde_json::Value::String(s)]
                        }
                        _ => Vec::new(),
                    };
                    net.insert("public_origins".into(), serde_json::Value::Array(moved));
                    changed = true;
                }
            }
        }
        if let Some(oidc) = doc.get_mut("oidc").and_then(|v| v.as_object_mut()) {
            if !oidc.contains_key("redirect_uris") {
                if let Some(old) = oidc.remove("redirect_uri") {
                    let moved = match old {
                        serde_json::Value::String(s) if !s.trim().is_empty() => {
                            vec![serde_json::Value::String(s)]
                        }
                        _ => Vec::new(),
                    };
                    oidc.insert("redirect_uris".into(), serde_json::Value::Array(moved));
                    changed = true;
                }
            }
        }
        changed.then(|| doc.to_string())
    }

    /// The full current override set, applied on top of `config.toml` at
    /// boot (`Config::apply_settings_overrides`) and re-read by the settings
    /// screen's `GET` to show each field's live/effective value.
    pub fn load(&self) -> SettingsOverrides {
        self.cached.lock().clone()
    }

    /// Persists first, then updates the cache — same ordering
    /// `IndexSettingsStore::set_name_enabled` uses, so a disk-write failure
    /// never lets the in-memory value drift from what's on disk. `mutate`
    /// gets the current value and returns the new one, so a caller only
    /// needs to touch the one section it's changing.
    pub fn mutate(
        &self,
        mutate: impl FnOnce(&mut SettingsOverrides),
    ) -> anyhow::Result<SettingsOverrides> {
        let mut next = self.cached.lock().clone();
        mutate(&mut next);
        let json = serde_json::to_string(&next)?;
        {
            let conn = self.db.lock();
            conn.execute(
                "INSERT INTO settings_overrides (id, json) VALUES (1, ?1)
                 ON CONFLICT(id) DO UPDATE SET json = excluded.json",
                rusqlite::params![json],
            )?;
        }
        *self.cached.lock() = next.clone();
        Ok(next)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn absent_row_is_all_none() {
        let store = SettingsStore::open_in_memory().unwrap();
        let o = store.load();
        assert!(o.search.is_none());
        assert!(o.smb.is_none());
    }

    #[test]
    fn mutate_persists_and_updates_cache() {
        let store = SettingsStore::open_in_memory().unwrap();
        store
            .mutate(|o| {
                o.archive_max_concurrent = Some(8);
            })
            .unwrap();
        assert_eq!(store.load().archive_max_concurrent, Some(8));
    }

    /// A row written before the rename keeps every one of its ten sections.
    /// Without the migration the network blob fails to deserialize and
    /// `unwrap_or_default()` throws all of them away, silently.
    #[test]
    fn a_pre_rename_row_is_migrated_rather_than_discarded() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("settings.db");
        let legacy = r#"{
            "network":{"bind":"0.0.0.0:8443","app_hosts":["nas.local"],"content_hosts":[],
                "allowed_origins":[],"trusted_proxies":[],
                "compat_canonical_url":"https://cloud.example.com"},
            "archive_max_concurrent":7,
            "oidc":{"enabled":true,"issuer":"https://idp.example.com","client_id":"sc",
                "redirect_uri":"https://cloud.example.com/api/auth/oidc/callback",
                "scopes":["openid"],"display_name":"","allow_private_endpoints":false,
                "smb_policy":"block"}
        }"#;
        {
            let conn = Connection::open(&path).unwrap();
            conn.execute_batch(SCHEMA).unwrap();
            conn.execute(
                "INSERT INTO settings_overrides (id, json) VALUES (1, ?1)",
                rusqlite::params![legacy],
            )
            .unwrap();
        }

        let store = SettingsStore::open(&path).unwrap();
        let o = store.load();
        let net = o.network.expect("the network section must survive");
        assert_eq!(net.public_origins, vec!["https://cloud.example.com".to_string()]);
        assert_eq!(net.app_hosts, vec!["nas.local".to_string()]);
        assert_eq!(o.archive_max_concurrent, Some(7));
        assert_eq!(
            o.oidc.expect("the oidc section must survive").redirect_uris,
            vec!["https://cloud.example.com/api/auth/oidc/callback".to_string()]
        );

        // Written back, so it runs once and leaves no legacy key behind.
        let reopened = SettingsStore::open(&path).unwrap();
        assert_eq!(
            reopened.load().network.unwrap().public_origins,
            vec!["https://cloud.example.com".to_string()]
        );
    }

    /// An administrator who cleared the box meant "no declared origin", and
    /// `[]` is what says that — not a one-element list holding nothing.
    #[test]
    fn a_cleared_canonical_url_migrates_to_an_empty_list() {
        let migrated = SettingsStore::migrate_renamed_keys(
            r#"{"network":{"bind":"0.0.0.0:8443","app_hosts":[],"content_hosts":[],
                "allowed_origins":[],"trusted_proxies":[],"compat_canonical_url":null}}"#,
        )
        .expect("a legacy row must be rewritten");
        let v: serde_json::Value = serde_json::from_str(&migrated).unwrap();
        assert_eq!(v["network"]["public_origins"], serde_json::json!([]));
        assert!(v["network"].get("compat_canonical_url").is_none());

        // And running it again is a no-op.
        assert!(SettingsStore::migrate_renamed_keys(&migrated).is_none());
    }

    /// A network section that needs nothing must not stop the oidc section
    /// being migrated: the two are independent, and an early return in the
    /// first would have silently left the second to fail deserialization and
    /// take all ten sections down with it.
    #[test]
    fn each_section_migrates_independently_of_the_other() {
        let migrated = SettingsStore::migrate_renamed_keys(
            r#"{"network":{"bind":"0.0.0.0:8443","app_hosts":[],"content_hosts":[],
                "allowed_origins":[],"trusted_proxies":[],"public_origins":[]},
                "oidc":{"enabled":false,"issuer":"","client_id":"",
                "redirect_uri":"https://cloud.example.com/cb","scopes":[],
                "display_name":"","allow_private_endpoints":false,"smb_policy":"block"}}"#,
        )
        .expect("the oidc half still needs rewriting");
        let v: serde_json::Value = serde_json::from_str(&migrated).unwrap();
        assert_eq!(
            v["oidc"]["redirect_uris"],
            serde_json::json!(["https://cloud.example.com/cb"])
        );
        assert_eq!(v["network"]["public_origins"], serde_json::json!([]));
    }

    #[test]
    fn survives_reopen_on_a_real_file() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("settings.db");
        {
            let store = SettingsStore::open(&path).unwrap();
            store
                .mutate(|o| o.archive_max_concurrent = Some(2))
                .unwrap();
        }
        let reopened = SettingsStore::open(&path).unwrap();
        assert_eq!(reopened.load().archive_max_concurrent, Some(2));
    }
}
