use crate::db::now_ns;
use crate::AuthService;
use sc_vfs::UserId;
use std::net::IpAddr;
use std::sync::Arc;

/// One `audit` row, as read back by
/// [`AuthService::list_audit`]. `rowid` is SQLite's implicit one — the table
/// declares no explicit primary key — used only as an opaque pagination
/// cursor, never shown as a stable public id.
///
/// Safe to serialize wholesale to an admin client: every `audit()` call site
/// in this codebase was written to keep `target`/`detail` free of secrets
/// (`setup.rs::audit_failure`'s doc comment states this explicitly for the
/// one call site that handles attacker-controlled input) — there is no
/// password hash, session token, or app-password value ever passed as either
/// field, so no field-level scrubbing is needed here.
#[derive(Clone, Debug)]
pub struct AuditRow {
    pub rowid: i64,
    pub ts_ns: i64,
    pub actor: Option<u32>,
    pub event: String,
    pub target: Option<String>,
    pub ip: Option<String>,
    pub ok: bool,
    pub detail: Option<String>,
}

/// Filters for [`AuthService::list_audit`]. `None` on any field means
/// unfiltered on that dimension.
#[derive(Clone, Debug, Default)]
pub struct AuditFilter {
    pub actor: Option<UserId>,
    pub event: Option<String>,
    pub since_ns: Option<i64>,
    pub until_ns: Option<i64>,
}

/// Rows per page, hard-capped regardless of what a caller requests — the
/// audit table has no upper bound on how far back a wide-open filter (no
/// actor, no event, no time bound) could scan.
pub const AUDIT_PAGE_MAX: u32 = 200;

/// `audit`'s stated retention ("Retention defaults to
/// 180 days"). Not yet an admin-configurable setting — YAGNI until a second
/// value is actually needed — but the docs promise pruning happens, so
/// [`AuthService::audit_prune`] must actually be called (see
/// `sc-server`'s `spawn_audit_sweeper`, which mirrors the Login Flow v2 row
/// sweeper that this exact "wrote the function, never called it" mistake
/// already caused there).
pub const AUDIT_RETENTION_NS: i64 = 180 * 24 * 60 * 60 * 1_000_000_000;

impl AuthService {
    /// Best-effort audit log insert. Never propagates
    /// errors — an audit-log write failure must not break the request it is
    /// describing.
    pub fn audit(
        &self,
        actor: Option<UserId>,
        event: &str,
        target: Option<&str>,
        ip: Option<IpAddr>,
        ok: bool,
        detail: Option<&str>,
    ) {
        let res = (|| -> anyhow::Result<()> {
            let conn = self.pool.get()?;
            conn.execute(
                "INSERT INTO audit (ts_ns, actor, event, target, ip, ua, result, detail) \
                 VALUES (?1, ?2, ?3, ?4, ?5, NULL, ?6, ?7)",
                rusqlite::params![
                    now_ns(),
                    actor.map(|u| u.get()),
                    event,
                    target,
                    ip.map(|i| i.to_string()),
                    if ok { 0i64 } else { 1i64 },
                    detail,
                ],
            )?;
            Ok(())
        })();
        if let Err(e) = res {
            tracing::warn!(error = %e, event, "audit log insert failed");
        }
    }

    /// How many audit rows carry `event`, optionally narrowed to successes
    /// (`Some(true)`) or failures (`Some(false)`).
    ///
    /// specifies the table but no reader, so this is the
    /// smallest read API that lets a caller assert a path actually recorded
    /// itself — used today by the tests that guard the first-run bootstrap,
    /// and the obvious foundation for the admin-facing view later.
    pub fn audit_count(&self, event: &str, ok: Option<bool>) -> anyhow::Result<u64> {
        let conn = self.pool.get()?;
        let n: i64 = match ok {
            Some(ok) => conn.query_row(
                "SELECT COUNT(*) FROM audit WHERE event = ?1 AND result = ?2",
                rusqlite::params![event, if ok { 0i64 } else { 1i64 }],
                |r| r.get(0),
            )?,
            None => conn.query_row(
                "SELECT COUNT(*) FROM audit WHERE event = ?1",
                rusqlite::params![event],
                |r| r.get(0),
            )?,
        };
        Ok(n as u64)
    }

    /// Admin-facing audit read (`FEATURES.md` #158). Newest first, keyed on
    /// `rowid` rather than an offset — a page boundary then stays correct
    /// even while new rows keep landing ahead of it. `before_rowid` is the
    /// last row's `rowid` from the previous page (exclusive); omit it for the
    /// first page. `limit` is clamped to `AUDIT_PAGE_MAX` regardless of what
    /// is asked for.
    pub fn list_audit(&self, filter: &AuditFilter, before_rowid: Option<i64>, limit: u32) -> anyhow::Result<Vec<AuditRow>> {
        let conn = self.pool.get()?;
        let limit = limit.clamp(1, AUDIT_PAGE_MAX);

        let mut sql = String::from("SELECT rowid, ts_ns, actor, event, target, ip, result, detail FROM audit WHERE 1=1");
        let mut params: Vec<Box<dyn rusqlite::ToSql>> = Vec::new();
        if let Some(actor) = filter.actor {
            sql.push_str(" AND actor = ?");
            params.push(Box::new(actor.get()));
        }
        if let Some(event) = &filter.event {
            sql.push_str(" AND event = ?");
            params.push(Box::new(event.clone()));
        }
        if let Some(since) = filter.since_ns {
            sql.push_str(" AND ts_ns >= ?");
            params.push(Box::new(since));
        }
        if let Some(until) = filter.until_ns {
            sql.push_str(" AND ts_ns <= ?");
            params.push(Box::new(until));
        }
        if let Some(before) = before_rowid {
            sql.push_str(" AND rowid < ?");
            params.push(Box::new(before));
        }
        sql.push_str(" ORDER BY rowid DESC LIMIT ?");
        params.push(Box::new(limit));

        let mut stmt = conn.prepare(&sql)?;
        let param_refs: Vec<&dyn rusqlite::ToSql> = params.iter().map(|p| p.as_ref()).collect();
        let rows = stmt.query_map(param_refs.as_slice(), |r| {
            Ok(AuditRow {
                rowid: r.get(0)?,
                ts_ns: r.get(1)?,
                actor: r.get::<_, Option<i64>>(2)?.map(|v| v as u32),
                event: r.get(3)?,
                target: r.get(4)?,
                ip: r.get(5)?,
                ok: r.get::<_, i64>(6)? == 0,
                detail: r.get(7)?,
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }

    /// Deletes every audit row older than `cutoff_ns`, returning the count
    /// removed. Nothing calls this on its own — `sc-server`'s
    /// `spawn_audit_sweeper` is the one periodic caller, mirroring
    /// `spawn_login_flow_sweeper`'s "the function existed but nothing invoked
    /// it, so the table grew forever" fix.
    pub fn audit_prune(&self, cutoff_ns: i64) -> anyhow::Result<u64> {
        let conn = self.pool.get()?;
        let n = conn.execute("DELETE FROM audit WHERE ts_ns < ?1", rusqlite::params![cutoff_ns])?;
        Ok(n as u64)
    }

    /// Periodic `audit_prune` at `AUDIT_RETENTION_NS`, mirroring
    /// `nc.rs::Compat::spawn_login_flow_sweeper`: a detached `tokio::spawn`
    /// loop, not stopped at shutdown, since a prune pass touches nothing
    /// shutdown also touches. Hourly is frequent enough for a 180-day
    /// retention window without scanning the table constantly.
    ///
    /// It also sweeps expired `oidc_flow` rows, which is not what the name
    /// says. This is the only periodic loop in the crate, and a cleanup
    /// function with no caller is the exact mistake both this sweeper and the
    /// Login Flow v2 one were written to correct. Abandoned flows are already
    /// swept opportunistically by `take_oidc_flow`; this covers the
    /// deployment where nobody ever completes a login, and hourly is ample
    /// for a ten-minute TTL that is enforced at read time regardless.
    pub fn spawn_audit_sweeper(self: &Arc<Self>) -> tokio::task::JoinHandle<()> {
        let auth = self.clone();
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(std::time::Duration::from_secs(60 * 60));
            loop {
                tick.tick().await;
                let cutoff = now_ns() - AUDIT_RETENTION_NS;
                match auth.audit_prune(cutoff) {
                    Ok(n) if n > 0 => tracing::debug!("pruned {n} audit rows past retention"),
                    Ok(_) => {}
                    Err(e) => tracing::warn!(error = %e, "audit prune failed"),
                }
                match auth.sweep_oidc_flows() {
                    Ok(n) if n > 0 => tracing::debug!("swept {n} expired oidc flows"),
                    Ok(_) => {}
                    Err(e) => tracing::warn!(error = %e, "oidc flow sweep failed"),
                }
            }
        })
    }
}
