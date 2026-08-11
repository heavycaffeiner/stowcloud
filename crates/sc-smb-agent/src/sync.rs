//! One apply: read what `sc-core` rendered, decide the scope, validate,
//! promote, reconcile accounts, and tell smbd as little as will do.
//!
//! Every failure keeps the previously promoted config running. A rejected
//! candidate, a colliding account or an unreadable interface list all leave
//! smbd serving what it was already serving, and turn into a [`Report`] the
//! caller sends back to `sc-core` rather than a log line nobody reads.

use std::path::{Path, PathBuf};
use std::process::Command;

use parking_lot::Mutex;
use sc_smb::agent::{Report, SmbdAction};

use crate::accounts;
use crate::conf;
use crate::scope;
use crate::smbd::{Mode, Smbd};

/// Everything this agent reads or writes, so a test can point it at a
/// temporary directory and so the bare-metal install can move the two paths
/// that differ there.
#[derive(Clone, Debug)]
pub struct Paths {
    /// What `sc-core` writes. Mounted read-only.
    pub config_dir: PathBuf,
    /// Scratch: the candidate config and the testparm output.
    pub state_dir: PathBuf,
    /// What smbd reads.
    pub smb_conf: PathBuf,
    pub passdb: PathBuf,
    pub passwd: PathBuf,
    pub group: PathBuf,
}

impl Default for Paths {
    fn default() -> Self {
        Self {
            config_dir: PathBuf::from("/config/smb"),
            state_dir: PathBuf::from("/var/lib/sc-smb-agent"),
            smb_conf: PathBuf::from("/etc/samba/smb.conf"),
            passdb: PathBuf::from("/var/lib/samba/private/passdb.tdb"),
            passwd: PathBuf::from("/etc/passwd"),
            group: PathBuf::from("/etc/group"),
        }
    }
}

impl Paths {
    fn rendered_conf(&self) -> PathBuf {
        self.config_dir.join("smb.conf")
    }
    fn rendered_passwd(&self) -> PathBuf {
        self.config_dir.join("passwd")
    }
    fn smbpasswd(&self) -> PathBuf {
        self.config_dir.join("smbpasswd")
    }
    fn policy(&self) -> PathBuf {
        self.config_dir.join("network.policy")
    }
    fn candidate(&self) -> PathBuf {
        self.state_dir.join("smb.conf.candidate")
    }
}

struct State {
    smbd: Smbd,
    /// The `interfaces` line the running smbd was started with. Compared, not
    /// assumed: it is the one directive a reload cannot apply.
    bound: String,
    /// The config as promoted last time, so an unchanged one costs nothing.
    promoted: String,
    last: Report,
}

pub struct Agent {
    paths: Paths,
    state: Mutex<State>,
}

/// smbd binds its listening sockets at startup and a reload does not revisit
/// them, so this is the whole of "reload will not do".
pub fn needs_restart(bound: &str, wanted: &str) -> bool {
    bound != wanted
}

impl Agent {
    pub fn new(paths: Paths, mode: Mode) -> Self {
        Self {
            paths,
            state: Mutex::new(State {
                smbd: Smbd::new(mode),
                bound: String::new(),
                promoted: String::new(),
                last: Report::default(),
            }),
        }
    }

    /// The last apply's answer, for `Request::Status`.
    pub fn last(&self) -> Report {
        self.state.lock().last.clone()
    }

    /// Whether smbd is up. Only the supervising loop asks: nothing else is
    /// watching a process this agent owns.
    pub fn smbd_running(&self) -> bool {
        self.state.lock().smbd.running()
    }

    /// Brute-force mitigation, on top of `hosts allow` and SMB3-required
    /// auth rather than instead of them.
    pub fn start_fail2ban(&self) {
        self.state.lock().smbd.start_fail2ban();
    }

    /// Cheap "has anything changed" for the poll loop: the rendered files'
    /// sizes and modification times, plus the detected scope, which moves
    /// when a VPN or a VLAN comes up without anything on disk changing.
    pub fn fingerprint(&self) -> String {
        let mut out = String::new();
        for name in ["smb.conf", "smbpasswd", "passwd", "network.policy"] {
            match std::fs::metadata(self.paths.config_dir.join(name)) {
                Ok(m) => {
                    let mtime = m
                        .modified()
                        .ok()
                        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                        .map(|d| d.as_nanos())
                        .unwrap_or(0);
                    out.push_str(&format!("{}:{mtime};", m.len()));
                }
                Err(_) => out.push_str("absent;"),
            }
        }
        let policy = conf::read_policy(&self.paths.policy());
        if !policy.pinned_interfaces {
            if let Ok(s) = scope::detect(policy.allow_public_bind) {
                out.push_str(&s.interfaces);
            }
        }
        out
    }

    pub fn apply(&self) -> Report {
        let mut st = self.state.lock();
        let report = self.apply_locked(&mut st);
        st.last = report.clone();
        report
    }

    fn apply_locked(&self, st: &mut State) -> Report {
        if let Err(e) = std::fs::create_dir_all(&self.paths.state_dir) {
            return Report::failed(format!("state directory: {e}"));
        }

        let src = match std::fs::read_to_string(self.paths.rendered_conf()) {
            Ok(s) => s,
            // Absence is the off switch: `sc-core` removes the rendered files
            // when `smb.enabled` goes false, and reading that as "not synced
            // yet" would leave SMB serving a revoked configuration.
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return self.teardown(st),
            Err(e) => return Report::failed(format!("reading the rendered smb.conf: {e}")),
        };

        let policy = conf::read_policy(&self.paths.policy());
        let mut warnings: Vec<String> = Vec::new();

        let scope = if policy.pinned_interfaces {
            None
        } else {
            match scope::detect(policy.allow_public_bind) {
                Ok(s) => {
                    if !s.detected {
                        warnings.push(
                            "no usable network interface was found, so SMB answers on loopback \
                             only; check that this container sees the host's network"
                                .to_string(),
                        );
                    }
                    Some(s)
                }
                Err(e) => {
                    // Not promoted: a scope this agent could not read is not a
                    // scope, and the previous config is still serving.
                    return Report::failed(format!("reading this machine's interfaces: {e}"));
                }
            }
        };

        let candidate = conf::candidate(&src, scope.as_ref());
        if let Err(e) = std::fs::write(self.paths.candidate(), &candidate) {
            return Report::failed(format!("writing the candidate config: {e}"));
        }
        if let Err(e) = testparm(&self.paths.candidate()) {
            return Report::failed(format!("{e}; keeping the previous config"));
        }

        // Accounts are checked before anything is promoted, because a refusal
        // here means the rendered roster cannot be applied at all.
        let rendered_passwd = std::fs::read_to_string(self.paths.rendered_passwd()).unwrap_or_default();
        let desired = accounts::parse_rendered(&rendered_passwd);
        let current_passwd = match std::fs::read_to_string(&self.paths.passwd) {
            Ok(s) => s,
            Err(e) => return Report::failed(format!("reading {}: {e}", self.paths.passwd.display())),
        };
        let group_file = std::fs::read_to_string(&self.paths.group).unwrap_or_default();
        let collisions = accounts::collisions(&desired, &current_passwd);
        if !collisions.is_empty() {
            return Report::failed(format!("refusing to sync: {}", collisions.join("; ")));
        }
        let missing_groups = accounts::missing_groups(&desired, &group_file);
        if !missing_groups.is_empty() {
            return Report::failed(format!(
                "refusing to sync: no group for gid {} (smb_service_gid)",
                missing_groups.join(", ")
            ));
        }

        // ---- promote ----
        if let Err(e) = std::fs::write(&self.paths.smb_conf, &candidate) {
            return Report::failed(format!("promoting {}: {e}", self.paths.smb_conf.display()));
        }

        if let Err(e) = accounts::write_passwd(
            &self.paths.passwd,
            &accounts::rebuild(&current_passwd, &desired),
        ) {
            warnings.push(format!("rebuilding {}: {e}", self.paths.passwd.display()));
        }
        if self.paths.smbpasswd().exists() {
            if let Err(e) = accounts::import(&self.paths.smbpasswd(), &self.paths.passdb) {
                warnings.push(e.to_string());
            }
        }
        if let Err(e) = accounts::prune(&desired) {
            warnings.push(format!("pruning the passdb: {e}"));
        }
        let missing_passdb = accounts::missing_passdb(&desired).unwrap_or_default();
        if !missing_passdb.is_empty() {
            warnings.push(format!(
                "no passdb entry for {}: they cannot authenticate over SMB",
                missing_passdb.join(", ")
            ));
        }

        // ---- what smbd is now serving ----
        let sections = conf::sections(&candidate);
        let missing_paths: Vec<String> = sections
            .iter()
            .filter(|s| !s.path.is_empty() && !Path::new(&s.path).is_dir())
            .map(|s| s.path.clone())
            .collect();
        if !missing_paths.is_empty() {
            warnings.push(format!(
                "these share paths do not exist here, so a client is told the network name is \
                 invalid: {}. Mount them into this container at the same paths.",
                missing_paths.join(", ")
            ));
        }

        let wanted = conf::bound_interfaces(&candidate);
        let smbd = self.settle(st, &candidate, &wanted, &mut warnings);
        st.smbd.nmbd(conf::netbios_wanted(&candidate));

        Report {
            ok: warnings.is_empty(),
            shares: sections.into_iter().map(|s| s.name).collect(),
            interfaces: wanted,
            hosts_allow: scope
                .map(|s| s.hosts_allow)
                .unwrap_or_else(|| hosts_allow_of(&candidate)),
            smbd,
            missing_paths,
            missing_passdb,
            error: (!warnings.is_empty()).then(|| warnings.join(" | ")),
        }
    }

    /// Tell smbd as little as will do, and no less.
    fn settle(
        &self,
        st: &mut State,
        candidate: &str,
        wanted: &str,
        warnings: &mut Vec<String>,
    ) -> SmbdAction {
        let running = st.smbd.running();
        let action = if !running {
            SmbdAction::Started
        } else if needs_restart(&st.bound, wanted) {
            SmbdAction::Restarted
        } else if st.promoted == candidate {
            SmbdAction::Unchanged
        } else {
            SmbdAction::Reloaded
        };

        let result = match action {
            SmbdAction::Started => st.smbd.start(),
            SmbdAction::Restarted => st.smbd.restart(),
            SmbdAction::Reloaded => st.smbd.reload(),
            _ => Ok(()),
        };
        match result {
            Ok(()) => {
                st.promoted = candidate.to_string();
                if matches!(action, SmbdAction::Started | SmbdAction::Restarted) {
                    st.bound = wanted.to_string();
                }
                action
            }
            Err(e) => {
                warnings.push(format!("smbd: {e}"));
                SmbdAction::Failed
            }
        }
    }

    /// `smb.enabled` went false, or the config directory was emptied. Stop
    /// serving and take the managed accounts and their NT hashes with it:
    /// leaving them behind would keep a revoked credential working.
    fn teardown(&self, st: &mut State) -> Report {
        let _ = st.smbd.stop();
        st.bound.clear();
        st.promoted.clear();
        let _ = accounts::prune(&[]);
        if let Ok(current) = std::fs::read_to_string(&self.paths.passwd) {
            let _ = accounts::write_passwd(&self.paths.passwd, &accounts::rebuild(&current, &[]));
        }
        Report {
            ok: true,
            smbd: SmbdAction::Stopped,
            ..Report::default()
        }
    }
}

/// Validate before it can reach smbd. `testparm` does not check that a
/// share's path exists, which is why [`Agent::apply_locked`] checks that
/// separately.
fn testparm(candidate: &Path) -> Result<(), String> {
    let out = Command::new("testparm")
        .arg("-s")
        .arg(candidate)
        .output()
        .map_err(|e| format!("running testparm: {e}"))?;
    if out.status.success() {
        return Ok(());
    }
    Err(format!(
        "testparm rejected the candidate config: {}",
        String::from_utf8_lossy(&out.stderr).trim()
    ))
}

fn hosts_allow_of(conf: &str) -> String {
    conf.lines()
        .filter_map(|l| {
            let t = l.trim_start();
            t.strip_prefix("hosts allow")
                .and_then(|r| r.trim_start().strip_prefix('='))
        })
        .next_back()
        .unwrap_or_default()
        .trim()
        .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_changed_interface_list_is_the_only_restart_trigger() {
        assert!(needs_restart("lo", "lo eth0"));
        assert!(!needs_restart("lo eth0", "lo eth0"));
    }

    #[test]
    fn hosts_allow_is_read_back_from_a_pinned_config() {
        let conf = "[global]\n  hosts allow = 10.0.0.0/8 192.168.0.0/16\n";
        assert_eq!(hosts_allow_of(conf), "10.0.0.0/8 192.168.0.0/16");
    }

    /// The off switch: no rendered config means stop, not "wait".
    #[test]
    fn a_missing_config_tears_down_rather_than_waiting() {
        let dir = tempfile::tempdir().unwrap();
        let paths = Paths {
            config_dir: dir.path().join("cfg"),
            state_dir: dir.path().join("state"),
            smb_conf: dir.path().join("smb.conf"),
            passdb: dir.path().join("passdb.tdb"),
            passwd: dir.path().join("passwd"),
            group: dir.path().join("group"),
        };
        std::fs::create_dir_all(&paths.config_dir).unwrap();
        std::fs::write(&paths.passwd, "root:x:0:0:root:/root:/bin/sh\n").unwrap();

        let agent = Agent::new(paths.clone(), Mode::Supervise);
        let report = agent.apply();
        assert_eq!(report.smbd, SmbdAction::Stopped);
        assert!(report.ok);
        // And the host's own accounts survived it.
        let passwd = std::fs::read_to_string(&paths.passwd).unwrap();
        assert!(passwd.contains("root:x:0:0"));
    }
}
