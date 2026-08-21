//! `/etc/passwd` reconciliation and the Samba passdb.
//!
//! Samba resolves an SMB login to a Unix account through `getpwnam`, so every
//! account named in a share needs a passwd entry even though `force user`
//! means none of them ever owns a file. `sc-core` renders those entries; this
//! puts them where `getpwnam` looks, and imports the matching NT hashes.
//!
//! Rebuilt from scratch on every pass rather than diffed, which is what makes
//! an account removed from `sc-core`'s registry disappear here too instead of
//! lingering as a stale entry forever.

use std::collections::BTreeSet;
use std::io;
use std::path::Path;
use std::process::Command;

/// Written into the GECOS field of every account this agent creates, and the
/// only thing that makes one eligible for removal. An account without it is
/// somebody else's and is never touched.
const MARKER: &str = "sc-managed-smb";

/// One rendered `passwd` line, split far enough to reason about.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Entry {
    pub name: String,
    pub uid: String,
    pub gid: String,
    /// The whole line, marker already stamped.
    pub line: String,
}

/// Parse what `sc-core` rendered. Anything that is not a seven-field passwd
/// line is dropped rather than passed through: this content ends up in
/// `/etc/passwd`, and a malformed line there breaks `getpwnam` for every
/// account after it.
pub fn parse_rendered(body: &str) -> Vec<Entry> {
    let mut out = Vec::new();
    for line in body.lines() {
        if line.trim().is_empty() {
            continue;
        }
        let mut f: Vec<&str> = line.split(':').collect();
        if f.len() < 7 || f[0].is_empty() {
            continue;
        }
        let (name, uid, gid) = (f[0].to_string(), f[2].to_string(), f[3].to_string());
        f[4] = MARKER;
        out.push(Entry {
            name,
            uid,
            gid,
            line: f.join(":"),
        });
    }
    out
}

/// `sc-core` account names reach `useradd`-shaped territory here, so they are
/// held to the portable-username rule before anything writes them into
/// `/etc/passwd`. A name starting with `-` would also be argument injection
/// into `pdbedit`.
pub fn valid_name(name: &str) -> bool {
    let mut chars = name.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    if !(first.is_ascii_lowercase() || first == '_') {
        return false;
    }
    if name.len() > 32 {
        return false;
    }
    chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_' || c == '-')
}

/// Whether a passwd line is one of ours.
fn managed(line: &str) -> bool {
    line.split(':').nth(4) == Some(MARKER)
}

/// Refuse to sync at all if an SMB account collides with a pre-existing
/// system account we did not create.
///
/// Adopting one silently would give that account's name an NT hash, and
/// removing it later would delete a system user. The uid half is not
/// cosmetic either: `pdbedit -i` resolves an smbpasswd line to an account by
/// uid and takes whatever name `getpwuid` answers with, so a shared uid
/// attaches the credential to the wrong name.
pub fn collisions(desired: &[Entry], passwd: &str) -> Vec<String> {
    let mut out = Vec::new();
    for e in desired {
        if !valid_name(&e.name) {
            out.push(format!("'{}' is not a valid system account name", e.name));
            continue;
        }
        for line in passwd.lines().filter(|l| !managed(l)) {
            let mut f = line.split(':');
            let (name, _, uid) = (f.next(), f.next(), f.next());
            if name == Some(e.name.as_str()) {
                out.push(format!("'{}' already exists as a non-managed system account", e.name));
            }
            if uid == Some(e.uid.as_str()) {
                out.push(format!(
                    "uid {} is already '{}', a non-managed account: move smb.service_uid clear of the host's real users",
                    e.uid,
                    name.unwrap_or("?")
                ));
            }
        }
    }
    out.sort();
    out.dedup();
    out
}

/// Every group the rendered accounts reference must already exist: this agent
/// does not invent groups, and an account whose gid resolves to nothing
/// breaks `getpwnam` just as surely as a missing account.
pub fn missing_groups(desired: &[Entry], group_file: &str) -> Vec<String> {
    let have: BTreeSet<&str> = group_file
        .lines()
        .filter_map(|l| l.split(':').nth(2))
        .collect();
    let mut out: Vec<String> = desired
        .iter()
        .map(|e| e.gid.clone())
        .filter(|g| !have.contains(g.as_str()))
        .collect();
    out.sort();
    out.dedup();
    out
}

/// The new `/etc/passwd`: everything that is not ours, then ours.
pub fn rebuild(current: &str, desired: &[Entry]) -> String {
    let mut out = String::with_capacity(current.len());
    for line in current.lines().filter(|l| !managed(l)) {
        out.push_str(line);
        out.push('\n');
    }
    for e in desired {
        out.push_str(&e.line);
        out.push('\n');
    }
    out
}

/// Write `/etc/passwd` through a temporary file and a rename, so a reader
/// never sees a partial one. That still leaves the ordinary lost-update race
/// against a concurrent `useradd`; shadow's own lock is not reachable here,
/// and a file server's SMB roster is not a multi-writer passwd.
pub fn write_passwd(path: &Path, body: &str) -> io::Result<()> {
    let tmp = path.with_extension("sc-smb-agent.tmp");
    std::fs::write(&tmp, body)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644))?;
    }
    std::fs::rename(&tmp, path)
}

/// Import the rendered NT hashes.
pub fn import(smbpasswd: &Path, passdb: &Path) -> io::Result<()> {
    let out = Command::new("pdbedit")
        .arg("-i")
        .arg(format!("smbpasswd:{}", smbpasswd.display()))
        .arg("-e")
        .arg(format!("tdbsam:{}", passdb.display()))
        .output()?;
    if !out.status.success() {
        return Err(io::Error::other(format!(
            "pdbedit import failed: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )));
    }
    Ok(())
}

/// Accounts the passdb currently holds.
pub fn passdb_names() -> io::Result<Vec<String>> {
    let out = Command::new("pdbedit").arg("-L").output()?;
    if !out.status.success() {
        return Err(io::Error::other("pdbedit -L failed"));
    }
    Ok(String::from_utf8_lossy(&out.stdout)
        .lines()
        .filter_map(|l| l.split(':').next())
        .filter(|n| !n.trim().is_empty())
        .map(|n| n.to_string())
        .collect())
}

/// Drop NT hashes for accounts that are no longer ours. Without this,
/// disabling a user in `sc-core` would leave them able to authenticate over
/// SMB with the credential that was just revoked.
pub fn prune(desired: &[Entry]) -> io::Result<Vec<String>> {
    let keep: BTreeSet<&str> = desired.iter().map(|e| e.name.as_str()).collect();
    let mut removed = Vec::new();
    for name in passdb_names()? {
        if keep.contains(name.as_str()) {
            continue;
        }
        let ok = Command::new("pdbedit")
            .arg("-x")
            .arg("-u")
            .arg(&name)
            .output()
            .map(|o| o.status.success())
            .unwrap_or(false);
        if ok {
            removed.push(name);
        }
    }
    Ok(removed)
}

/// Accounts that should be able to authenticate and cannot.
///
/// `pdbedit -i` reports success and imports nothing when the smbpasswd line's
/// uid field names no passwd entry: no error, no output, exit 0, an empty
/// passdb. Every downstream symptom (a client told the password is wrong,
/// `NT_STATUS_NO_SUCH_USER` in the smbd log) points at credentials or config
/// rather than at the import, so it is checked here where the cause is still
/// visible.
pub fn missing_passdb(desired: &[Entry]) -> io::Result<Vec<String>> {
    let known: BTreeSet<String> = passdb_names()?.into_iter().collect();
    Ok(desired
        .iter()
        .map(|e| e.name.clone())
        .filter(|n| !known.contains(n))
        .collect())
}

#[cfg(test)]
mod tests {
    use super::*;

    const RENDERED: &str = "alice:x:2001:1000::/nonexistent:/sbin/nologin\n\
                            bob:x:2002:1000::/nonexistent:/sbin/nologin\n";

    const SYSTEM: &str = "root:x:0:0:root:/root:/bin/sh\n\
                          scsvc:x:1000:1000::/nonexistent:/sbin/nologin\n";

    #[test]
    fn rendered_entries_get_the_marker() {
        let e = parse_rendered(RENDERED);
        assert_eq!(e.len(), 2);
        assert_eq!(e[0].name, "alice");
        assert_eq!(e[0].uid, "2001");
        assert_eq!(e[0].gid, "1000");
        assert!(e[0].line.contains(MARKER), "{}", e[0].line);
    }

    #[test]
    fn a_malformed_line_never_reaches_etc_passwd() {
        let e = parse_rendered("alice:x:2001\n\nbob:x:2002:1000::/nonexistent:/sbin/nologin\n");
        assert_eq!(e.len(), 1);
        assert_eq!(e[0].name, "bob");
    }

    /// The property the whole marker scheme exists for: an account dropped
    /// from the registry disappears rather than accumulating.
    #[test]
    fn rebuilding_drops_managed_accounts_that_are_gone() {
        let first = rebuild(SYSTEM, &parse_rendered(RENDERED));
        assert!(first.contains("alice"));
        assert!(first.contains("bob"));

        let second = rebuild(&first, &parse_rendered("alice:x:2001:1000::/nonexistent:/sbin/nologin\n"));
        assert!(second.contains("alice"));
        assert!(!second.contains("bob"));
        // And never at the cost of the host's own accounts.
        assert!(second.contains("root:x:0:0"));
        assert!(second.contains("scsvc:x:1000"));
    }

    #[test]
    fn a_name_collision_with_a_real_account_is_refused() {
        let bad = parse_rendered("scsvc:x:2001:1000::/nonexistent:/sbin/nologin\n");
        let c = collisions(&bad, SYSTEM);
        assert!(c.iter().any(|m| m.contains("non-managed system account")), "{c:?}");
    }

    /// The expensive one: `pdbedit -i` resolves by uid, so a shared uid
    /// silently attaches the credential to the wrong name.
    #[test]
    fn a_uid_collision_with_a_real_account_is_refused() {
        let bad = parse_rendered("alice:x:1000:1000::/nonexistent:/sbin/nologin\n");
        let c = collisions(&bad, SYSTEM);
        assert!(c.iter().any(|m| m.contains("uid 1000")), "{c:?}");
    }

    #[test]
    fn our_own_previous_entries_do_not_count_as_collisions() {
        let desired = parse_rendered(RENDERED);
        let passwd = rebuild(SYSTEM, &desired);
        assert!(collisions(&desired, &passwd).is_empty());
    }

    #[test]
    fn names_are_held_to_the_portable_rule() {
        assert!(valid_name("alice"));
        assert!(valid_name("_svc-1"));
        assert!(!valid_name(""));
        assert!(!valid_name("-x"));
        assert!(!valid_name("Alice"));
        assert!(!valid_name("alice bob"));
        assert!(!valid_name(&"a".repeat(33)));
    }

    #[test]
    fn a_gid_with_no_group_is_reported() {
        let g = "root:x:0:\nscsvc:x:1000:\n";
        assert!(missing_groups(&parse_rendered(RENDERED), g).is_empty());
        let orphan = parse_rendered("carol:x:2003:4242::/nonexistent:/sbin/nologin\n");
        assert_eq!(missing_groups(&orphan, g), vec!["4242".to_string()]);
    }

    #[test]
    fn writing_passwd_replaces_it_atomically() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("passwd");
        std::fs::write(&path, SYSTEM).unwrap();
        write_passwd(&path, &rebuild(SYSTEM, &parse_rendered(RENDERED))).unwrap();
        let back = std::fs::read_to_string(&path).unwrap();
        assert!(back.contains("alice"));
        assert!(back.contains("root"));
        assert!(!dir.path().join("passwd.sc-smb-agent.tmp").exists());
    }
}
