//! `sc-meta` — the SQLite cache that sits on top of the filesystem.
//!
//! The filesystem is the only source of truth (`ARCHITECTURE.md` §0.1): this
//! database can be deleted at any time and the service keeps working, just
//! slower until it warms back up. Everything in here exists to answer three
//! questions cheaply that the kernel answers expensively or not at all:
//!
//!   1. "what stable integer id does this (dev, ino, btime) have?" (`node`,
//!      §4.1 of `ARCHITECTURE.md`)
//!   2. "has anything under this directory changed?" (`diretag`, §4.2 /
//!      `DESIGN-CORE.md` §4)
//!   3. "what dead WebDAV properties are attached to this file?" (`dav_prop`)
//!
//! Schema is exactly `node`/`diretag` as specified in `ARCHITECTURE.md`
//! §4.1/§4.2: **no path column**, **no index besides `node_ident`**. Path
//! resolution is always `parent`-chain walking, never a stored string —
//! that's what makes directory rename an `O(1)` single-row `UPDATE` instead
//! of an `O(subtree)` fan-out (`DESIGN-FOOTPRINT.md` §2).

mod admin;
mod etag;
mod node;
mod props;

#[cfg(test)]
mod tests;

pub use etag::Aggregate;
pub use props::DavProp;

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Mutex;

use r2d2::{Pool, PooledConnection};
use r2d2_sqlite::SqliteConnectionManager;
use rusqlite::{Connection, OpenFlags};

/// `node.flags` bit: this row represents a directory.
pub(crate) const NODE_FLAG_IS_DIR: i64 = 1 << 0;
/// `node.flags` bit: something else (dead property, lock, favorite, share
/// link — `ARCHITECTURE.md` §4.1) references this row by fileid, so GC must
/// not reap it even if the underlying `(dev, ino)` looks gone. Set by
/// `set_prop`; nothing currently clears it automatically (a stale pin just
/// means "GC skips this row a little longer than strictly necessary", never
/// data loss).
pub(crate) const NODE_FLAG_PINNED: i64 = 1 << 1;

const SCHEMA_SQL: &str = "
CREATE TABLE IF NOT EXISTS node (
  id       INTEGER PRIMARY KEY,
  share    INTEGER NOT NULL,
  parent   INTEGER NOT NULL,
  name     TEXT    NOT NULL,
  dev      INTEGER NOT NULL,
  ino      INTEGER NOT NULL,
  btime_ns INTEGER,
  flags    INTEGER NOT NULL,
  size     INTEGER,
  mtime_ns INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS node_ident ON node(share, dev, ino, btime_ns);

CREATE TABLE IF NOT EXISTS diretag (
  share  INTEGER NOT NULL,
  fileid INTEGER NOT NULL,
  etag   TEXT    NOT NULL,
  rsize  INTEGER NOT NULL,
  rcount INTEGER NOT NULL,
  gen    INTEGER NOT NULL,
  valid  INTEGER NOT NULL,
  PRIMARY KEY (share, fileid)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS dav_prop (
  fileid INTEGER NOT NULL,
  ns     TEXT NOT NULL,
  name   TEXT NOT NULL,
  value  TEXT NOT NULL,
  PRIMARY KEY (fileid, ns, name)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS share_gen (
  share INTEGER PRIMARY KEY,
  gen   INTEGER NOT NULL
) WITHOUT ROWID;
";

/// Pragmas that are safe (indeed required) to (re)apply on *every* pooled
/// connection: `busy_timeout`/`cache_size`/`temp_store` are per-connection
/// session settings, and `journal_mode`/`synchronous`/`wal_autocheckpoint`/
/// `journal_size_limit` are cheap idempotent no-ops when already set.
///
/// Deliberately excludes `page_size` and `auto_vacuum` — those two are
/// database-level, must be set before the first table is created, and are
/// applied exactly once via the bootstrap connection in `open_inner`
/// (`DESIGN-FOOTPRINT.md` §4, "SQLite configuration").
fn apply_common_pragmas(conn: &Connection) -> rusqlite::Result<()> {
    // `busy_timeout` leads deliberately. It governs how every statement after
    // it behaves under contention, and `journal_mode` is the one that needs an
    // exclusive lock — setting the timeout afterwards means the one pragma
    // most likely to contend is the one running without it. This is applied on
    // eight pooled connections, so the ordering is not theoretical: the same
    // mistake in `sc-auth` produced `database is locked` on a fresh database.
    conn.execute_batch(
        "PRAGMA busy_timeout = 5000;
         PRAGMA journal_mode = WAL;
         PRAGMA synchronous = NORMAL;
         PRAGMA wal_autocheckpoint = 1000;
         PRAGMA journal_size_limit = 67108864;
         PRAGMA cache_size = -16000;
         PRAGMA temp_store = MEMORY;",
    )
}

enum Target {
    File(PathBuf),
    /// A `file:<name>?mode=memory&cache=shared` URI. Plain `:memory:` gives
    /// every pool connection its own private, empty database, which breaks
    /// the pool model entirely — shared-cache is the only way an r2d2 pool
    /// of in-memory connections can see the same data.
    Memory(String),
}

pub struct MetaStore {
    pool: Pool<SqliteConnectionManager>,
    /// For the `Memory` target only: a connection to the shared-cache
    /// database that we hold open for the lifetime of the `MetaStore`. A
    /// shared-cache `:memory:` database is destroyed the instant its last
    /// connection closes; without this, an idle pool could drop the data
    /// out from under later calls.
    _keepalive: Mutex<Option<Connection>>,
    /// `DESIGN-FOOTPRINT.md` §4.4's always-on free-space floor. Driven from
    /// outside (`sc-server`'s sampler, which is the only thing that knows
    /// what volume this database sits on and what `db.min_free_bytes` is);
    /// `sc-meta` never touches the filesystem itself.
    writes_blocked: AtomicBool,
}

impl MetaStore {
    /// Open (creating if necessary) a file-backed store. Pragmas — including
    /// `page_size` and `auto_vacuum`, which only take effect before any
    /// table exists — are applied before schema creation.
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::open_inner(Target::File(path.to_path_buf()))
    }

    /// A shared-cache in-memory store, for tests. Each call gets its own
    /// private namespace so parallel tests never collide.
    pub fn open_in_memory() -> anyhow::Result<Self> {
        static COUNTER: AtomicU64 = AtomicU64::new(0);
        let n = COUNTER.fetch_add(1, Ordering::Relaxed);
        let uri = format!(
            "file:sc_meta_mem_{}_{n}?mode=memory&cache=shared",
            std::process::id()
        );
        Self::open_inner(Target::Memory(uri))
    }

    fn open_inner(target: Target) -> anyhow::Result<Self> {
        let bootstrap = match &target {
            Target::File(p) => Connection::open(p)?,
            Target::Memory(uri) => Connection::open_with_flags(
                uri,
                OpenFlags::SQLITE_OPEN_READ_WRITE
                    | OpenFlags::SQLITE_OPEN_CREATE
                    | OpenFlags::SQLITE_OPEN_URI,
            )?,
        };

        // MUST happen before any table is created (DESIGN-FOOTPRINT.md §4).
        // Silently ignored by SQLite on a database that already has tables,
        // which makes re-opening an existing store harmless.
        bootstrap.execute_batch("PRAGMA page_size = 4096; PRAGMA auto_vacuum = INCREMENTAL;")?;
        apply_common_pragmas(&bootstrap)?;
        bootstrap.execute_batch(SCHEMA_SQL)?;

        let auto_vacuum: i64 = bootstrap.query_row("PRAGMA auto_vacuum", [], |r| r.get(0))?;
        anyhow::ensure!(
            auto_vacuum == 2,
            "auto_vacuum did not engage (got {auto_vacuum}, expected 2/INCREMENTAL) — \
             pragma-before-schema ordering was violated"
        );

        let manager = match &target {
            Target::File(p) => SqliteConnectionManager::file(p),
            Target::Memory(uri) => SqliteConnectionManager::file(uri).with_flags(
                OpenFlags::SQLITE_OPEN_READ_WRITE
                    | OpenFlags::SQLITE_OPEN_CREATE
                    | OpenFlags::SQLITE_OPEN_URI,
            ),
        }
        .with_init(|conn| apply_common_pragmas(conn));

        let pool = Pool::builder().max_size(8).build(manager)?;

        let keepalive = match target {
            Target::File(_) => None,
            Target::Memory(_) => Some(bootstrap),
        };

        Ok(Self {
            pool,
            _keepalive: Mutex::new(keepalive),
            writes_blocked: AtomicBool::new(false),
        })
    }

    pub(crate) fn conn(&self) -> anyhow::Result<PooledConnection<SqliteConnectionManager>> {
        Ok(self.pool.get()?)
    }

    /// Stop (or resume) the writes that make this database *bigger*.
    ///
    /// `DESIGN-FOOTPRINT.md` §4.4: independent of `db.size_guard`, and not
    /// turn-off-able by policy, because it is what stands between a full
    /// volume and a corrupt SQLite file. The caller is `sc-server`'s
    /// free-space sampler.
    ///
    /// Only *growth* is gated, and deliberately so — the store keeps serving
    /// while the floor holds:
    ///
    /// - gated: [`MetaStore::fileid`] (INSERTs a `node` row) and
    ///   [`MetaStore::set_prop`] (INSERTs a `dav_prop` row).
    /// - not gated, reclaims space: `del_prop`, `gc_dead_nodes`. Blocking
    ///   these would block recovery, which is backwards.
    /// - not gated, no growth: `rename_node` and `bump_share_gen` update one
    ///   existing row. Blocking `rename_node` would leave the id -> path
    ///   mapping pointing at the old path, which is worse than a slightly
    ///   larger file.
    /// - not gated, correctness: `put_dir_etag` and `mark_dirty_chain`.
    ///   Refusing them leaves a cached directory ETag that is stale *and*
    ///   still flagged `valid`, and clients would then be told nothing
    ///   changed when it did. A wrong answer is not an acceptable way to
    ///   save a page.
    ///
    /// Reads are never gated at all.
    pub fn set_writes_blocked(&self, blocked: bool) {
        self.writes_blocked.store(blocked, Ordering::Relaxed);
    }

    pub fn writes_blocked(&self) -> bool {
        self.writes_blocked.load(Ordering::Relaxed)
    }

    pub(crate) fn ensure_writable(&self) -> anyhow::Result<()> {
        anyhow::ensure!(
            !self.writes_blocked(),
            "metadata store is read-only: free space on its volume is below \
             db.min_free_bytes (DESIGN-FOOTPRINT.md §4.4)"
        );
        Ok(())
    }
}
