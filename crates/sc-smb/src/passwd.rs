//! `/etc/passwd`-style entries for the sidecar (`DEPLOYMENT.md` §7.2 passdb
//! sync, point 3): Samba requires `getpwnam` to succeed for every SMB user,
//! so the sidecar synthesizes one entry per user.
//!
//! Each entry carries its **own** uid ([`SmbUser::uid`]) and the **shared**
//! service gid. The uid has to be unique because `pdbedit -i` matches an
//! smbpasswd line to a Unix account by uid. The gid is shared because that is
//! what the connecting token is checked against for the group ACE on a 0775
//! share root (§7.3).
//!
//! Neither decides who can read what: real access control stays in
//! `valid users` / `read list` / `write list`, and file ownership comes from
//! `force user`/`force group = scsvc`.

use crate::SmbUser;

/// Render `name:x:uid:gid::/nonexistent:/usr/sbin/nologin` for each user.
pub fn render_passwd_entries(users: &[SmbUser], gid: u32) -> String {
    let mut out = String::new();
    for u in users {
        out.push_str(&format!(
            "{name}:x:{uid}:{gid}::/nonexistent:/usr/sbin/nologin\n",
            name = u.name,
            uid = u.uid,
            gid = gid,
        ));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// One uid per account, one gid for all of them. This asserted the
    /// opposite — every entry on the service uid — until a real two-account
    /// deployment showed `pdbedit -i` importing only one of them.
    #[test]
    fn each_entry_gets_its_own_uid_and_the_shared_gid() {
        let users = vec![
            SmbUser {
                name: "alice".into(),
                uid: 1001,
            },
            SmbUser {
                name: "bob".into(),
                uid: 1002,
            },
        ];
        let out = render_passwd_entries(&users, 1000);
        let lines: Vec<&str> = out.lines().collect();
        assert_eq!(lines.len(), 2);
        assert!(lines[0].starts_with("alice:x:1001:1000:"));
        assert!(lines[1].starts_with("bob:x:1002:1000:"));
    }
}
