//! Group CRUD + membership. Owns the `group_` and
//! `membership` tables. Mirrors `users.rs`'s shape: thin SQL wrappers on
//! `AuthService`, no ACL knowledge here — `sc_core::Core::set_group_memberships`
//! is the one place membership data crosses into `sc-acl`'s live engine.

use crate::{AuthService, GroupNameError, GroupOpError, GroupRow};
use anyhow::Result;
use sc_vfs::{GroupId, UserId};
use std::collections::HashMap;

impl AuthService {
    pub fn create_group(&self, name: &str) -> std::result::Result<GroupId, GroupNameError> {
        let conn = self.pool.get().map_err(|e| GroupNameError::Internal(e.to_string()))?;
        let inserted = conn.execute("INSERT INTO group_ (name) VALUES (?1)", rusqlite::params![name]);
        let id = match inserted {
            Ok(_) => GroupId::new(conn.last_insert_rowid() as u32),
            Err(rusqlite::Error::SqliteFailure(e, _)) if e.code == rusqlite::ErrorCode::ConstraintViolation => {
                return Err(GroupNameError::DuplicateName);
            }
            Err(e) => return Err(GroupNameError::Internal(e.to_string())),
        };
        drop(conn);
        self.audit(None, "admin.group_created", Some(name), None, true, None);
        Ok(id)
    }

    /// Every group, ordered by id.
    pub fn list_groups(&self) -> Result<Vec<GroupRow>> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare("SELECT id, name FROM group_ ORDER BY id")?;
        let rows = stmt.query_map([], |r| {
            Ok(GroupRow { id: GroupId::new(r.get::<_, i64>(0)? as u32), name: r.get(1)? })
        })?;
        let mut out = Vec::new();
        for row in rows {
            out.push(row?);
        }
        Ok(out)
    }

    pub fn rename_group(&self, id: GroupId, name: &str) -> std::result::Result<(), GroupNameError> {
        let conn = self.pool.get().map_err(|e| GroupNameError::Internal(e.to_string()))?;
        let n = conn.execute("UPDATE group_ SET name = ?1 WHERE id = ?2", rusqlite::params![name, id.get()]);
        let n = match n {
            Ok(n) => n,
            Err(rusqlite::Error::SqliteFailure(e, _)) if e.code == rusqlite::ErrorCode::ConstraintViolation => {
                return Err(GroupNameError::DuplicateName);
            }
            Err(e) => return Err(GroupNameError::Internal(e.to_string())),
        };
        if n == 0 {
            return Err(GroupNameError::NotFound);
        }
        drop(conn);
        self.audit(None, "admin.group_renamed", Some(name), None, true, None);
        Ok(())
    }

    /// Cascades to `membership` inside one transaction — same reasoning as
    /// `delete_user`: neither table declares `ON DELETE CASCADE` (`db.rs`),
    /// so a crash mid-delete must not be able to leave a group id in
    /// `membership` that no longer exists in `group_`.
    pub fn delete_group(&self, id: GroupId) -> std::result::Result<(), GroupOpError> {
        let mut conn = self.pool.get().map_err(|e| GroupOpError::Internal(e.to_string()))?;
        let tx = conn.transaction().map_err(|e| GroupOpError::Internal(e.to_string()))?;
        let n = tx
            .execute("DELETE FROM group_ WHERE id = ?1", rusqlite::params![id.get()])
            .map_err(|e| GroupOpError::Internal(e.to_string()))?;
        if n == 0 {
            return Err(GroupOpError::NotFound);
        }
        tx.execute("DELETE FROM membership WHERE group_ = ?1", rusqlite::params![id.get()])
            .map_err(|e| GroupOpError::Internal(e.to_string()))?;
        tx.commit().map_err(|e| GroupOpError::Internal(e.to_string()))?;
        drop(conn);
        // Every grant naming this group just became inert, which changes who
        // `smb.conf` lists in `valid users`. Nothing rewrites that file on
        // its own.
        self.republish_passdb();
        self.audit(None, "admin.group_deleted", None, None, true, None);
        Ok(())
    }

    /// Adds `user` to `group`, refusing if either id doesn't exist —
    /// `membership` has no foreign key, so an unchecked insert would leave a
    /// row naming a phantom account or group that `list_memberships_all`
    /// would then silently hand to the ACL engine.
    pub fn add_membership(&self, user: UserId, group: GroupId) -> std::result::Result<(), GroupOpError> {
        let conn = self.pool.get().map_err(|e| GroupOpError::Internal(e.to_string()))?;
        self.check_user_and_group_exist(&conn, user, group)?;
        conn.execute(
            "INSERT OR IGNORE INTO membership (user, group_) VALUES (?1, ?2)",
            rusqlite::params![user.get(), group.get()],
        )
        .map_err(|e| GroupOpError::Internal(e.to_string()))?;
        drop(conn);
        // Membership decides which grants reach this account, so it decides
        // which shares name them in `valid users`.
        self.republish_passdb();
        self.audit(Some(user), "admin.group_member_added", None, None, true, None);
        Ok(())
    }

    pub fn remove_membership(&self, user: UserId, group: GroupId) -> std::result::Result<(), GroupOpError> {
        let conn = self.pool.get().map_err(|e| GroupOpError::Internal(e.to_string()))?;
        self.check_user_and_group_exist(&conn, user, group)?;
        conn.execute(
            "DELETE FROM membership WHERE user = ?1 AND group_ = ?2",
            rusqlite::params![user.get(), group.get()],
        )
        .map_err(|e| GroupOpError::Internal(e.to_string()))?;
        drop(conn);
        // Removal is the direction that matters: without this the account
        // keeps the group's SMB access until something else republishes.
        self.republish_passdb();
        self.audit(Some(user), "admin.group_member_removed", None, None, true, None);
        Ok(())
    }

    fn check_user_and_group_exist(
        &self,
        conn: &rusqlite::Connection,
        user: UserId,
        group: GroupId,
    ) -> std::result::Result<(), GroupOpError> {
        let user_exists: bool = conn
            .query_row("SELECT EXISTS(SELECT 1 FROM user WHERE id = ?1)", rusqlite::params![user.get()], |r| r.get(0))
            .map_err(|e| GroupOpError::Internal(e.to_string()))?;
        let group_exists: bool = conn
            .query_row("SELECT EXISTS(SELECT 1 FROM group_ WHERE id = ?1)", rusqlite::params![group.get()], |r| r.get(0))
            .map_err(|e| GroupOpError::Internal(e.to_string()))?;
        if !user_exists || !group_exists {
            return Err(GroupOpError::NotFound);
        }
        Ok(())
    }

    /// This group's current members, by id.
    pub fn list_group_members(&self, group: GroupId) -> Result<Vec<UserId>> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare("SELECT user FROM membership WHERE group_ = ?1 ORDER BY user")?;
        let rows = stmt.query_map(rusqlite::params![group.get()], |r| Ok(UserId::new(r.get::<_, i64>(0)? as u32)))?;
        let mut out = Vec::new();
        for row in rows {
            out.push(row?);
        }
        Ok(out)
    }

    /// Every account's group memberships, in one shot — the accessor
    /// `sc_core::Core::set_group_memberships`'s doc comment (and
    /// `sc-server::app::project_grants`) has been waiting for. Empty groups
    /// and users with no memberships simply don't appear as keys, which is
    /// exactly what `sc_acl::AclEngine::set_memberships` expects (a lookup
    /// miss already means "no groups").
    pub fn list_memberships_all(&self) -> Result<HashMap<UserId, Vec<GroupId>>> {
        let conn = self.pool.get()?;
        let mut stmt = conn.prepare("SELECT user, group_ FROM membership ORDER BY user")?;
        let rows = stmt.query_map([], |r| {
            Ok((UserId::new(r.get::<_, i64>(0)? as u32), GroupId::new(r.get::<_, i64>(1)? as u32)))
        })?;
        let mut out: HashMap<UserId, Vec<GroupId>> = HashMap::new();
        for row in rows {
            let (user, group) = row?;
            out.entry(user).or_default().push(group);
        }
        Ok(out)
    }
}
