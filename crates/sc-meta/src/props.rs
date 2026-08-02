//! WebDAV dead properties (`PROPPATCH`-set arbitrary properties). Stored
//! keyed by fileid, never as filesystem xattrs — writing xattrs would
//! collide with other services/backup tools touching the same tree
//! Dead WebDAV properties, stored by fileid so a rename carries them.

use sc_vfs::FileId;
use rusqlite::params;

use crate::{MetaStore, NODE_FLAG_PINNED};

#[derive(Clone, Debug)]
pub struct DavProp {
    pub ns: String,
    pub name: String,
    pub value: String,
}

impl MetaStore {
    pub fn get_props(&self, id: FileId) -> anyhow::Result<Vec<DavProp>> {
        let conn = self.conn()?;
        let mut stmt =
            conn.prepare("SELECT ns, name, value FROM dav_prop WHERE fileid = ?1 ORDER BY ns, name")?;
        let rows = stmt
            .query_map(params![id.get()], |r| {
                Ok(DavProp {
                    ns: r.get(0)?,
                    name: r.get(1)?,
                    value: r.get(2)?,
                })
            })?
            .collect::<Result<Vec<_>, _>>()?;
        Ok(rows)
    }

    /// Setting a property pins the node ('s "there's a
    /// reason it can't be deleted" bit) so `gc_dead_nodes` won't reap it out
    /// from under the property that references it.
    pub fn set_prop(&self, id: FileId, ns: &str, name: &str, value: &str) -> anyhow::Result<()> {
        self.ensure_writable()?;
        let conn = self.conn()?;
        conn.execute(
            "INSERT INTO dav_prop(fileid, ns, name, value) VALUES (?1, ?2, ?3, ?4) \
             ON CONFLICT(fileid, ns, name) DO UPDATE SET value = excluded.value",
            params![id.get(), ns, name, value],
        )?;
        conn.execute(
            "UPDATE node SET flags = flags | ?1 WHERE id = ?2",
            params![NODE_FLAG_PINNED, id.get()],
        )?;
        Ok(())
    }

    pub fn del_prop(&self, id: FileId, ns: &str, name: &str) -> anyhow::Result<()> {
        let conn = self.conn()?;
        conn.execute(
            "DELETE FROM dav_prop WHERE fileid = ?1 AND ns = ?2 AND name = ?3",
            params![id.get(), ns, name],
        )?;
        Ok(())
    }
}
