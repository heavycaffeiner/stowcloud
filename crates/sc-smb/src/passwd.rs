//! `/etc/passwd`-style entries for the sidecar (`DEPLOYMENT.md` §7.2 passdb
//! sync, point 3): Samba requires `getpwnam` to succeed for every SMB user,
//! so the sidecar synthesizes one entry per user, **all sharing the same
//! uid** (our service uid) — real access control stays in `valid users` /
//! `read list` / `write list`, this is purely to satisfy `getpwnam`.

use crate::SmbUser;

/// Render `name:x:uid:gid::/nonexistent:/usr/sbin/nologin` for each user.
pub fn render_passwd_entries(users: &[SmbUser], uid: u32, gid: u32) -> String {
    let mut out = String::new();
    for u in users {
        out.push_str(&format!(
            "{name}:x:{uid}:{gid}::/nonexistent:/usr/sbin/nologin\n",
            name = u.name,
            uid = uid,
            gid = gid,
        ));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn all_entries_share_one_uid() {
        let users = vec![
            SmbUser { name: "alice".into() },
            SmbUser { name: "bob".into() },
        ];
        let out = render_passwd_entries(&users, 1000, 1000);
        let lines: Vec<&str> = out.lines().collect();
        assert_eq!(lines.len(), 2);
        assert!(lines[0].starts_with("alice:x:1000:1000:"));
        assert!(lines[1].starts_with("bob:x:1000:1000:"));
    }
}
