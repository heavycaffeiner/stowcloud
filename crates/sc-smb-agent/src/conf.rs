//! Turning what `sc-core` rendered into what smbd runs.
//!
//! Three edits and two questions. The edits: a log target smbd's own config
//! has no reason to carry, the two scope lines, and nothing else. The
//! questions: which shares are in there, and does this config want a NetBIOS
//! name, both of which decide what happens to processes afterwards.

use std::path::Path;

use crate::scope::Scope;

/// What the sidecar needs from `sc-core` to decide the network scope.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Policy {
    pub allow_public_bind: bool,
    /// The operator named the addresses in `smb.interfaces`, so the rendered
    /// scope lines are final and detection must not widen them.
    pub pinned_interfaces: bool,
}

/// A missing or unreadable policy file is the closed reading of both flags:
/// neither is something to assume in the permissive direction because a file
/// did not arrive.
pub fn read_policy(path: &Path) -> Policy {
    let Ok(body) = std::fs::read_to_string(path) else {
        return Policy::default();
    };
    Policy {
        allow_public_bind: body.lines().any(|l| l.trim() == "allow_public_bind=1"),
        pinned_interfaces: body.lines().any(|l| l.trim() == "pinned_interfaces=1"),
    }
}

/// fail2ban needs somewhere to tail, and `auth_audit:3` is the only level at
/// which smbd logs an authentication failure at all: plain `log level = 1`
/// never emits one, confirmed against a real bad-password connection. Raising
/// just that class keeps the global level at 1 rather than turning on smbd's
/// per-request chatter.
///
/// A single file, not the per-client `log.%m` some Samba examples use: the
/// fail2ban filter shipped beside this hardcodes `/var/log/samba/log.smbd`,
/// and a `%m`-split log would scatter auth failures across files it never
/// looks at.
const LOG_DIRECTIVES: &str = "  log file = /var/log/samba/log.smbd\n  log level = 1 auth_audit:3\n";

/// Build what gets validated and promoted. `scope` is `None` under a pin,
/// where the rendered lines are already the final answer.
///
/// The scope lines are substituted, never inserted: `sc-core` always renders
/// both, and a file missing them is one we should not be widening anyway.
pub fn candidate(src: &str, scope: Option<&Scope>) -> String {
    let mut out = String::with_capacity(src.len() + LOG_DIRECTIVES.len());
    for (i, line) in src.lines().enumerate() {
        if i == 0 {
            // `render_global` always emits `[global]` as the literal first
            // line, so this lands inside that section without this file
            // needing to know anything else about the shape.
            out.push_str(line);
            out.push('\n');
            out.push_str(LOG_DIRECTIVES);
            continue;
        }
        match scope {
            Some(s) if is_directive(line, "interfaces") => {
                out.push_str("  interfaces = ");
                out.push_str(&s.interfaces);
                out.push('\n');
            }
            Some(s) if is_directive(line, "hosts allow") => {
                out.push_str("  hosts allow = ");
                out.push_str(&s.hosts_allow);
                out.push('\n');
            }
            _ => {
                out.push_str(line);
                out.push('\n');
            }
        }
    }
    out
}

/// `  name = value`, with Samba's tolerance for whitespace but not for a
/// comment that happens to contain the word.
fn is_directive(line: &str, name: &str) -> bool {
    let t = line.trim_start();
    let Some(rest) = t.strip_prefix(name) else {
        return false;
    };
    rest.trim_start().starts_with('=')
}

fn directive_value<'a>(line: &'a str, name: &str) -> Option<&'a str> {
    if !is_directive(line, name) {
        return None;
    }
    line.split_once('=').map(|(_, v)| v.trim())
}

/// One `[section]` and the path it serves.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Section {
    pub name: String,
    pub path: String,
}

/// Every share in a config, `[global]` excluded.
///
/// Read back from the promoted file rather than tracked alongside it, so what
/// gets reported is what smbd is actually serving.
pub fn sections(conf: &str) -> Vec<Section> {
    let mut out: Vec<Section> = Vec::new();
    let mut current: Option<Section> = None;
    for line in conf.lines() {
        let t = line.trim();
        if let Some(name) = t.strip_prefix('[').and_then(|r| r.strip_suffix(']')) {
            if let Some(s) = current.take() {
                out.push(s);
            }
            if !name.eq_ignore_ascii_case("global") {
                current = Some(Section {
                    name: name.to_string(),
                    path: String::new(),
                });
            }
            continue;
        }
        if let (Some(s), Some(v)) = (current.as_mut(), directive_value(line, "path")) {
            s.path = v.to_string();
        }
    }
    if let Some(s) = current.take() {
        out.push(s);
    }
    out
}

/// Whether this config wants nmbd running. Read back from the promoted file
/// rather than from an environment variable, which is what lets a settings
/// change take effect on the next apply instead of the next restart.
pub fn netbios_wanted(conf: &str) -> bool {
    conf.lines()
        .filter_map(|l| directive_value(l, "disable netbios"))
        .next_back()
        .map(|v| !matches!(v.to_ascii_lowercase().as_str(), "yes" | "true" | "1"))
        .unwrap_or(false)
}

/// The `interfaces` line of a promoted config. What the running smbd bound,
/// which is the one thing a reload cannot change.
pub fn bound_interfaces(conf: &str) -> String {
    conf.lines()
        .filter_map(|l| directive_value(l, "interfaces"))
        .next_back()
        .unwrap_or_default()
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    const RENDERED: &str = "\
[global]
  workgroup = WORKGROUP
  disable netbios = yes
  bind interfaces only = yes
  interfaces = lo
  hosts allow = 127.0.0.0/8 ::1/128
  hosts deny = 0.0.0.0/0

[Share]
  path = /Tokyo/Share
  valid users = heavycaffeiner
";

    fn scope() -> Scope {
        Scope {
            interfaces: "lo eth0".into(),
            hosts_allow: "127.0.0.0/8 ::1/128 192.168.0.0/16".into(),
            detected: true,
        }
    }

    #[test]
    fn logging_lands_inside_global() {
        let out = candidate(RENDERED, None);
        let mut lines = out.lines();
        assert_eq!(lines.next(), Some("[global]"));
        assert_eq!(lines.next(), Some("  log file = /var/log/samba/log.smbd"));
        assert_eq!(lines.next(), Some("  log level = 1 auth_audit:3"));
    }

    #[test]
    fn the_scope_lines_are_replaced_and_nothing_else_is() {
        let out = candidate(RENDERED, Some(&scope()));
        assert!(out.contains("  interfaces = lo eth0\n"));
        assert!(out.contains("  hosts allow = 127.0.0.0/8 ::1/128 192.168.0.0/16\n"));
        assert!(out.contains("  hosts deny = 0.0.0.0/0\n"));
        assert!(out.contains("  workgroup = WORKGROUP\n"));
        assert_eq!(out.matches("interfaces =").count(), 2); // ours and `bind interfaces only`
    }

    /// A pin means `sc-core` already wrote the final answer.
    #[test]
    fn no_scope_leaves_the_rendered_lines_alone() {
        let out = candidate(RENDERED, None);
        assert!(out.contains("  interfaces = lo\n"));
        assert!(out.contains("  hosts allow = 127.0.0.0/8 ::1/128\n"));
    }

    #[test]
    fn sections_carry_their_paths_and_skip_global() {
        let s = sections(RENDERED);
        assert_eq!(s.len(), 1);
        assert_eq!(s[0].name, "Share");
        assert_eq!(s[0].path, "/Tokyo/Share");
    }

    #[test]
    fn a_commented_directive_is_not_one() {
        let conf = "[global]\n  # interfaces = lo eth9\n  interfaces = lo\n";
        assert_eq!(bound_interfaces(conf), "lo");
    }

    #[test]
    fn netbios_follows_the_promoted_file() {
        assert!(!netbios_wanted(RENDERED));
        assert!(netbios_wanted("[global]\n  disable netbios = no\n"));
        // Absent means sc-core rendered neither, which only happens for a
        // config this agent did not produce.
        assert!(!netbios_wanted("[global]\n"));
    }

    #[test]
    fn policy_defaults_closed_when_the_file_is_missing() {
        let dir = tempfile::tempdir().unwrap();
        let p = read_policy(&dir.path().join("network.policy"));
        assert!(!p.allow_public_bind);
        assert!(!p.pinned_interfaces);
    }

    #[test]
    fn policy_reads_both_flags() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("network.policy");
        std::fs::write(&path, "# comment\nallow_public_bind=1\npinned_interfaces=0\n").unwrap();
        let p = read_policy(&path);
        assert!(p.allow_public_bind);
        assert!(!p.pinned_interfaces);
    }
}
