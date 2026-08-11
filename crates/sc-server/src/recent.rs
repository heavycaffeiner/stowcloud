//! The recency query: what this account wrote here, newest first.
//!
//! The row supplies two things and only two: that a write happened, and when.
//! Everything on the screen (name, size, mtime, the fact that the file exists,
//! the fact that it is a file and not a directory) comes from a `stat`
//! performed while answering the request, through `Core`, under the same ACL
//! evaluation as every other read. A database that answered questions about
//! file state would be a cache pretending to be the filesystem.
//!
//! There is no walk here, no deadline and no truncation, so the answer is
//! exact: every file you wrote here inside the window, newest first, up to
//! `limit`.

use std::sync::Arc;

use sc_core::{SharePath, Vpath};
use sc_http::recent_api::{RecentApi, RecentHit, RecentQuery};
use sc_vfs::UserId;

use crate::journal::{WriteJournal, WriteRow};

pub struct RecentEngine {
    pub core: Arc<sc_core::Core>,
    /// `None` when `journal.db` could not be opened. The endpoint then answers
    /// an empty list rather than failing, which is the same best-effort
    /// promise every write path makes.
    pub journal: Option<Arc<WriteJournal>>,
}

impl RecentEngine {
    /// Resolve a supplied scope and refuse it when it does not resolve.
    ///
    /// Never widened to "everything": silently widening a scope is how a
    /// scoped endpoint becomes an unscoped read.
    fn scope_vpath(
        &self,
        user: UserId,
        scope: Option<&str>,
    ) -> Result<Option<Vpath>, sc_core::CoreError> {
        match scope {
            None => Ok(None),
            Some(s) => {
                let v = Vpath::new(s);
                self.core.resolve(user, &v)?;
                Ok(Some(v))
            }
        }
    }
}

impl RecentApi for RecentEngine {
    fn recent(
        &self,
        user: UserId,
        q: &RecentQuery,
    ) -> Result<Vec<RecentHit>, sc_http::core_api::CoreError> {
        let scope = self
            .scope_vpath(user, q.scope.as_deref())
            .map_err(crate::bridge::http_err)?;
        let Some(journal) = &self.journal else {
            return Ok(Vec::new());
        };

        let since_ns = i64::try_from(q.since_ns).unwrap_or(i64::MIN);
        let rows = journal.newest(user, since_ns);

        let mut hits = Vec::with_capacity(q.limit as usize);
        // Rows whose file the read proved gone. Every other reason for
        // dropping a row leaves the table alone: a revoked grant may be
        // granted again tomorrow, and a share that failed to mount at boot
        // would otherwise erase every account's history on the first page
        // load.
        let mut dead: Vec<WriteRow> = Vec::new();

        for row in rows {
            if hits.len() >= q.limit as usize {
                break;
            }
            // No filesystem here: `vpath_for` walks the caller's grants and
            // does prefix arithmetic. A row that no longer projects into a
            // vpath the caller holds is dropped from the answer and kept in
            // the table.
            let Ok(sp) = SharePath::parse(&row.path, u16::MAX) else {
                continue;
            };
            let Some(vpath) = self.core.vpath_for(user, row.share, &sp) else {
                continue;
            };
            if let Some(scope) = &scope {
                if !vpath.is_inside(scope) {
                    continue;
                }
            }
            let entry = match self.core.stat_entry(user, vpath.as_str()) {
                Ok(e) => e,
                Err(sc_core::CoreError::NotFound) => {
                    dead.push(row);
                    continue;
                }
                Err(_) => continue,
            };
            // A directory-level copy or move records the directory, not the
            // thousands of files under it; the files-only reader shows nothing
            // for one.
            if entry.kind != sc_vfs::Kind::File {
                continue;
            }
            let vpath = vpath.as_str().to_string();
            let share = vpath.split('/').next().unwrap_or_default().to_string();
            hits.push(RecentHit {
                share,
                name: entry.name,
                size: entry.size,
                mtime_ns: entry.mtime_ns,
                at_ns: i128::from(row.at_ns),
                op: row.op.as_str(),
                vpath,
            });
        }

        journal.forget(user, &dead);
        Ok(hits)
    }

    fn forget_user(&self, user: UserId) {
        if let Some(j) = &self.journal {
            j.forget_user(user);
        }
    }
}
