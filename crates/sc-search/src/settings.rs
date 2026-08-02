//! Persisted admin override for whether the T3 name index is turned on
//! ("both indexes are off
//! by default").
//!
//! This does *not* rewrite `config.toml`: an admin toggle from the web UI has
//! to survive a restart without editing a file the server doesn't own write
//! access to assume, and without racing an operator's own edits to it. The
//! precedent this follows is `sc-upload`'s `upload_chunk_settings` table (a
//! single row, `CHECK (id = 1)`, absence meaning "no override yet") rather
//! than `sc-core::ShareStore`'s own multi-table DB file: a lone boolean has
//! no relational shape that would justify a dedicated store with several
//! tables, but it is still not reconstructible from the filesystem
//! ('s "meta.db is a disposable cache" rule), so it
//! cannot live there either. It gets its own tiny file (`index.db`) rather
//! than a table bolted onto someone else's, because this crate has no other
//! reason to depend on any other crate's database.

use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};

use parking_lot::Mutex;
use rusqlite::{Connection, OptionalExtension};

const SCHEMA: &str = "\
    CREATE TABLE IF NOT EXISTS index_settings (\
        id           INTEGER PRIMARY KEY CHECK (id = 1),\
        name_enabled INTEGER NOT NULL\
    );";

pub struct IndexSettingsStore {
    db: Mutex<Connection>,
    /// Live cached value: every reader (the HTTP settings route, the
    /// admin-triggered build gate, the CLI's `ensure_name_index_enabled`)
    /// reads this instead of re-querying SQLite per call.
    name_enabled: AtomicBool,
}

impl IndexSettingsStore {
    /// `default_enabled` is `config.toml`'s `[index] name_enabled` — what a
    /// deployment with no admin override row yet still reports, and the
    /// starting value the first toggle overrides.
    pub fn open(path: &Path, default_enabled: bool) -> anyhow::Result<Self> {
        Self::from_conn(Connection::open(path)?, default_enabled)
    }

    /// Test-only constructor (no file on disk).
    pub fn open_in_memory(default_enabled: bool) -> anyhow::Result<Self> {
        Self::from_conn(Connection::open_in_memory()?, default_enabled)
    }

    fn from_conn(conn: Connection, default_enabled: bool) -> anyhow::Result<Self> {
        conn.pragma_update(None, "journal_mode", "WAL")?;
        conn.execute_batch("PRAGMA synchronous = NORMAL; PRAGMA busy_timeout = 5000;")?;
        conn.execute_batch(SCHEMA)?;
        let stored: Option<bool> = conn
            .query_row("SELECT name_enabled FROM index_settings WHERE id = 1", [], |r| {
                r.get::<_, i64>(0)
            })
            .optional()?
            .map(|v| v != 0);
        Ok(Self {
            db: Mutex::new(conn),
            name_enabled: AtomicBool::new(stored.unwrap_or(default_enabled)),
        })
    }

    pub fn name_enabled(&self) -> bool {
        self.name_enabled.load(Ordering::Relaxed)
    }

    /// Persists first, flips the live flag second — same ordering
    /// `sc-upload::UploadEngine::set_chunk_settings` uses, so a disk-write
    /// failure never lets the in-memory value drift from what's on disk.
    pub fn set_name_enabled(&self, enabled: bool) -> anyhow::Result<()> {
        {
            let conn = self.db.lock();
            conn.execute(
                "INSERT INTO index_settings (id, name_enabled) VALUES (1, ?1)
                 ON CONFLICT(id) DO UPDATE SET name_enabled = excluded.name_enabled",
                rusqlite::params![enabled as i64],
            )?;
        }
        self.name_enabled.store(enabled, Ordering::Relaxed);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn falls_back_to_config_default_with_no_row() {
        let store = IndexSettingsStore::open_in_memory(true).unwrap();
        assert!(store.name_enabled());
        let store = IndexSettingsStore::open_in_memory(false).unwrap();
        assert!(!store.name_enabled());
    }

    #[test]
    fn override_persists_across_reopen() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("index.db");
        {
            let store = IndexSettingsStore::open(&path, false).unwrap();
            assert!(!store.name_enabled());
            store.set_name_enabled(true).unwrap();
            assert!(store.name_enabled());
        }
        // A fresh connection to the same file sees the persisted row, not
        // `default_enabled` again.
        let reopened = IndexSettingsStore::open(&path, false).unwrap();
        assert!(reopened.name_enabled());
    }

    #[test]
    fn toggling_back_off_persists_too() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("index.db");
        let store = IndexSettingsStore::open(&path, true).unwrap();
        store.set_name_enabled(false).unwrap();
        let reopened = IndexSettingsStore::open(&path, true).unwrap();
        assert!(!reopened.name_enabled(), "an explicit off-override must not be masked by the config default");
    }
}
