//! Persisted admin overrides for the server-settings screen (`settings.db`).
//!
//! Same shape as `sc-search`'s `IndexSettingsStore` and `sc-upload`'s
//! `upload_chunk_settings`: a single row, `CHECK (id = 1)`, absence meaning
//! "no override yet". This one stores the whole [`crate::config::
//! SettingsOverrides`] as one JSON blob rather than one column per field —
//! unlike those two precedents, this store's shape grows with every config
//! section the settings screen covers, and a JSON blob is the one encoding
//! that doesn't need a schema migration each time a field is added to it.
//! It deliberately does **not** rewrite `config.toml`: that file is the
//! operator's and `scripts/deploy.sh` overwrites it on every push, so an
//! admin-UI edit written there would be silently reverted on the next
//! deploy.

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
            Some(json) => serde_json::from_str(&json).unwrap_or_default(),
            None => SettingsOverrides::default(),
        };
        Ok(Self {
            db: Mutex::new(conn),
            cached: Mutex::new(overrides),
        })
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
