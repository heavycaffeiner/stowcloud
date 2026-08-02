//! In-memory `CoreApi`/`MetaApi` so the protocol layer can be exercised with no
//! filesystem and no socket.

#![allow(dead_code)]

use std::collections::HashMap;
use std::sync::Arc;

use sc_dav::backend::{
    Aggregate, CoreApi, CoreError, CoreResult, DavProp, Entry, Listing, MetaApi, Order, Perms,
    Quota, Resolved, Sort,
};
use sc_vfs::{FileId, Kind, SafePath, ShareId, UserId};
use parking_lot::Mutex;

pub const SHARE: ShareId = ShareId(1);
pub const USER: UserId = UserId(7);

#[derive(Clone, Debug)]
pub struct Node {
    pub kind: Kind,
    pub data: Vec<u8>,
    pub mtime_ns: i128,
    pub id: FileId,
    pub perms: Perms,
}

#[derive(Default)]
pub struct MemCore {
    inner: Mutex<Inner>,
}

#[derive(Default)]
struct Inner {
    nodes: HashMap<String, Node>,
    next_id: i64,
    /// Paths the user may not even list — they must answer 404, not 403.
    unlistable: Vec<String>,
    quota: Option<Quota>,
}

impl MemCore {
    pub fn new() -> Arc<Self> {
        let c = Arc::new(MemCore::default());
        {
            let mut g = c.inner.lock();
            g.next_id = 1;
            g.quota = Some(Quota {
                used: 4096,
                available: Some(1_000_000),
            });
        }
        c.mkdir_raw("");
        c
    }

    fn alloc(&self) -> FileId {
        let mut g = self.inner.lock();
        g.next_id += 1;
        FileId(g.next_id)
    }

    pub fn mkdir_raw(&self, path: &str) {
        let id = self.alloc();
        self.inner.lock().nodes.insert(
            path.to_string(),
            Node {
                kind: Kind::Dir,
                data: Vec::new(),
                mtime_ns: 1_700_000_000_000_000_000,
                id,
                perms: Perms::all(),
            },
        );
    }

    pub fn file(&self, path: &str, data: &[u8]) {
        let id = self.alloc();
        self.inner.lock().nodes.insert(
            path.to_string(),
            Node {
                kind: Kind::File,
                data: data.to_vec(),
                mtime_ns: 1_700_000_000_000_000_000,
                id,
                perms: Perms::all(),
            },
        );
    }

    pub fn set_perms(&self, path: &str, p: Perms) {
        if let Some(n) = self.inner.lock().nodes.get_mut(path) {
            n.perms = p;
        }
    }

    pub fn make_unlistable(&self, path: &str) {
        self.inner.lock().unlistable.push(path.to_string());
    }

    pub fn set_quota(&self, q: Option<Quota>) {
        self.inner.lock().quota = q;
    }

    pub fn exists(&self, path: &str) -> bool {
        self.inner.lock().nodes.contains_key(path)
    }

    pub fn contents(&self, path: &str) -> Option<Vec<u8>> {
        self.inner.lock().nodes.get(path).map(|n| n.data.clone())
    }

    fn blocked(&self, path: &str) -> bool {
        self.inner
            .lock()
            .unlistable
            .iter()
            .any(|u| path == u || (path.starts_with(u.as_str()) && path.as_bytes().get(u.len()) == Some(&b'/')))
    }

    fn entry_of(&self, path: &str, n: &Node) -> Entry {
        let name = path.rsplit('/').next().unwrap_or(path).to_string();
        Entry {
            name,
            kind: n.kind,
            size: n.data.len() as u64,
            mtime_ns: n.mtime_ns,
            etag: format!("{}-{}-{}", n.id.0, n.data.len(), n.mtime_ns),
            perms: n.perms,
            id: Some(n.id),
            is_symlink_denied: false,
            confusable: false,
            btime_ns: Some(n.mtime_ns),
        }
    }
}

fn parent_of(p: &str) -> String {
    match p.rfind('/') {
        Some(i) => p[..i].to_string(),
        None => String::new(),
    }
}

impl CoreApi for MemCore {
    fn resolve(&self, _user: UserId, vpath: &str) -> CoreResult<Resolved> {
        if self.blocked(vpath) {
            return Err(CoreError::NotListable);
        }
        Ok(Resolved {
            share: SHARE,
            path: SafePath::parse(vpath, 64).map_err(CoreError::from)?,
            perms: Perms::all(),
        })
    }

    fn list(&self, _user: UserId, vpath: &str, _s: Sort, _o: Order) -> CoreResult<Listing> {
        if self.blocked(vpath) {
            return Err(CoreError::NotListable);
        }
        let g = self.inner.lock();
        let Some(n) = g.nodes.get(vpath) else {
            return Err(CoreError::NotFound);
        };
        if n.kind != Kind::Dir {
            return Err(CoreError::NotDir);
        }
        let mut entries: Vec<Entry> = g
            .nodes
            .iter()
            .filter(|(p, _)| !p.is_empty() && parent_of(p) == vpath)
            .map(|(p, n)| self.entry_of(p, n))
            .collect();
        entries.sort_by(|a, b| a.name.cmp(&b.name));
        let total = entries.len() as u64;
        Ok(Listing {
            entries,
            total,
            dir_etag: format!("dir-{}", n.id.0),
            listing_id: 1,
            cursor: None,
        })
    }

    fn stat_entry(&self, _user: UserId, vpath: &str) -> CoreResult<Entry> {
        if self.blocked(vpath) {
            return Err(CoreError::NotListable);
        }
        let g = self.inner.lock();
        match g.nodes.get(vpath) {
            Some(n) => Ok(self.entry_of(vpath, n)),
            None => Err(CoreError::NotFound),
        }
    }

    fn mkdir(&self, _user: UserId, vpath: &str) -> CoreResult<()> {
        if self.inner.lock().nodes.contains_key(vpath) {
            return Err(CoreError::Exists);
        }
        self.mkdir_raw(vpath);
        Ok(())
    }

    fn rename(&self, _user: UserId, from: &str, to: &str) -> CoreResult<()> {
        let mut g = self.inner.lock();
        let keys: Vec<String> = g
            .nodes
            .keys()
            .filter(|k| {
                *k == from || (k.starts_with(from) && k.as_bytes().get(from.len()) == Some(&b'/'))
            })
            .cloned()
            .collect();
        if keys.is_empty() {
            return Err(CoreError::NotFound);
        }
        for k in keys {
            let n = g.nodes.remove(&k).unwrap();
            let nk = format!("{to}{}", &k[from.len()..]);
            g.nodes.insert(nk, n);
        }
        Ok(())
    }

    fn move_entries(&self, user: UserId, from: &[String], to_dir: &str) -> CoreResult<()> {
        for f in from {
            let name = f.rsplit('/').next().unwrap_or(f);
            let to = if to_dir.is_empty() {
                name.to_string()
            } else {
                format!("{to_dir}/{name}")
            };
            self.rename(user, f, &to)?;
        }
        Ok(())
    }

    fn copy_entries(&self, _user: UserId, from: &[String], to_dir: &str) -> CoreResult<()> {
        for f in from {
            let name = f.rsplit('/').next().unwrap_or(f).to_string();
            let base = if to_dir.is_empty() {
                name.clone()
            } else {
                format!("{to_dir}/{name}")
            };
            let src: Vec<(String, Node)> = {
                let g = self.inner.lock();
                g.nodes
                    .iter()
                    .filter(|(k, _)| {
                        *k == f || (k.starts_with(f.as_str()) && k.as_bytes().get(f.len()) == Some(&b'/'))
                    })
                    .map(|(k, v)| (k.clone(), v.clone()))
                    .collect()
            };
            if src.is_empty() {
                return Err(CoreError::NotFound);
            }
            for (k, mut n) in src {
                n.id = self.alloc();
                let nk = format!("{base}{}", &k[f.len()..]);
                self.inner.lock().nodes.insert(nk, n);
            }
        }
        Ok(())
    }

    fn copy_to(&self, _user: UserId, from: &str, to: &str) -> CoreResult<()> {
        let src: Vec<(String, Node)> = {
            let g = self.inner.lock();
            g.nodes
                .iter()
                .filter(|(k, _)| {
                    k.as_str() == from
                        || (k.starts_with(from) && k.as_bytes().get(from.len()) == Some(&b'/'))
                })
                .map(|(k, v)| (k.clone(), v.clone()))
                .collect()
        };
        if src.is_empty() {
            return Err(CoreError::NotFound);
        }
        for (k, mut n) in src {
            n.id = self.alloc();
            let nk = format!("{to}{}", &k[from.len()..]);
            self.inner.lock().nodes.insert(nk, n);
        }
        Ok(())
    }

    fn delete(&self, _user: UserId, vpath: &str) -> CoreResult<()> {
        let mut g = self.inner.lock();
        let keys: Vec<String> = g
            .nodes
            .keys()
            .filter(|k| {
                *k == vpath
                    || (k.starts_with(vpath) && k.as_bytes().get(vpath.len()) == Some(&b'/'))
            })
            .cloned()
            .collect();
        if keys.is_empty() {
            return Err(CoreError::NotFound);
        }
        for k in keys {
            g.nodes.remove(&k);
        }
        Ok(())
    }

    fn read_text(&self, user: UserId, vpath: &str) -> CoreResult<String> {
        let b = self.read_bytes(user, vpath)?;
        String::from_utf8(b).map_err(|_| CoreError::Invalid("not utf-8".into()))
    }

    fn write_text(&self, user: UserId, vpath: &str, data: &str) -> CoreResult<()> {
        self.write_bytes(user, vpath, data.as_bytes())
    }

    fn read_bytes(&self, _user: UserId, vpath: &str) -> CoreResult<Vec<u8>> {
        if self.blocked(vpath) {
            return Err(CoreError::NotListable);
        }
        let g = self.inner.lock();
        match g.nodes.get(vpath) {
            Some(n) if n.kind == Kind::File => Ok(n.data.clone()),
            Some(_) => Err(CoreError::IsDir),
            None => Err(CoreError::NotFound),
        }
    }

    fn write_bytes(&self, _user: UserId, vpath: &str, data: &[u8]) -> CoreResult<()> {
        let id = self.alloc();
        let mut g = self.inner.lock();
        match g.nodes.get_mut(vpath) {
            Some(n) => {
                n.data = data.to_vec();
                n.mtime_ns += 1_000_000_000;
            }
            None => {
                g.nodes.insert(
                    vpath.to_string(),
                    Node {
                        kind: Kind::File,
                        data: data.to_vec(),
                        mtime_ns: 1_700_000_000_000_000_000,
                        id,
                        perms: Perms::all(),
                    },
                );
            }
        }
        Ok(())
    }

    fn aggregate(&self, _share: ShareId, path: &SafePath) -> anyhow::Result<Aggregate> {
        let p = path.to_display_string();
        let g = self.inner.lock();
        let mut rsize = 0u64;
        let mut rcount = 0u64;
        for (k, n) in g.nodes.iter() {
            if k == &p || (k.starts_with(&p) && k.as_bytes().get(p.len()) == Some(&b'/')) {
                rsize += n.data.len() as u64;
                rcount += 1;
            }
        }
        Ok(Aggregate {
            etag: format!("agg-{rcount}-{rsize}"),
            rsize,
            rcount,
        })
    }

    fn quota(&self, _user: UserId, _vpath: &str) -> CoreResult<Quota> {
        match self.inner.lock().quota {
            Some(q) => Ok(q),
            None => Ok(Quota {
                used: 0,
                available: None,
            }),
        }
    }
}

#[derive(Default)]
pub struct MemMeta {
    props: Mutex<HashMap<(i64, String, String), String>>,
}

impl MemMeta {
    pub fn new() -> Arc<Self> {
        Arc::new(MemMeta::default())
    }
}

impl MetaApi for MemMeta {
    fn get_props(&self, id: FileId) -> anyhow::Result<Vec<DavProp>> {
        Ok(self
            .props
            .lock()
            .iter()
            .filter(|((f, _, _), _)| *f == id.0)
            .map(|((_, ns, name), v)| DavProp {
                ns: ns.clone(),
                name: name.clone(),
                value: v.clone(),
            })
            .collect())
    }
    fn set_prop(&self, id: FileId, ns: &str, name: &str, value: &str) -> anyhow::Result<()> {
        self.props
            .lock()
            .insert((id.0, ns.to_string(), name.to_string()), value.to_string());
        Ok(())
    }
    fn del_prop(&self, id: FileId, ns: &str, name: &str) -> anyhow::Result<()> {
        self.props
            .lock()
            .remove(&(id.0, ns.to_string(), name.to_string()));
        Ok(())
    }
}
