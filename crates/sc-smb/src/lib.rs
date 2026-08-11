//! `sc-smb` — Samba (`smbd`) sidecar orchestration.
//!
//! This crate never talks to Samba binaries and never sees plaintext
//! passwords. It only:
//!
//!   1. Renders `smb.conf` (`generate_conf`) from `Share`/`Grant`-shaped
//!      input the caller already resolved — one Samba `[share]` per distinct
//!      subpath grant, since SMB shares are path-scoped and can't express a
//!      subpath restriction on their own.
//!   2. Writes the sidecar-facing files (`write_all`): `smb.conf`,
//!      an `smbpasswd`(5) file (rendered by the caller from
//!      `sc_auth::AuthService::export_smbpasswd()` — this crate never
//!      derives or touches NT hashes), `/etc/passwd`-style entries so
//!      Samba's `getpwnam` succeeds for every SMB user, and `network.policy`,
//!      the one-line handoff telling the sidecar whether public addresses may
//!      be bound.
//!
//! The sidecar itself (inotify-watch `/config/smb`, expand the network scope,
//! `testparm -s`, `smbcontrol reload-config`) is out of scope for this crate —
//! that's a separate small program/container that consumes the files this
//! crate writes.
//!
//! Reach is decided in the sidecar, not here. It enumerates the host's own
//! interfaces and expands `interfaces`/`hosts allow` from them; the config
//! rendered here binds loopback only, so an unexpanded file is closed.

pub mod agent;
mod bind;
mod conf;
mod error;
mod passwd;

pub use bind::{enclosing_private_range, is_private, PRIVATE_CIDRS_V4, PRIVATE_CIDRS_V6};
pub use error::SmbError;
pub use passwd::render_passwd_entries;

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};

use serde::{Deserialize, Serialize};

/// `smb.totp_policy`.
/// TOTP users cannot authenticate SMB with their account password — NTLM has
/// no slot for a second factor, so allowing it would be a silent 2FA bypass.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TotpPolicy {
    /// TOTP users must set a dedicated SMB password. Default.
    #[default]
    RequireSeparate,
    /// TOTP users cannot use SMB at all.
    Block,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct SmbConfig {
    /// SMB is off by default ("default is least privilege").
    pub enabled: bool,
    pub workgroup: String,
    /// NetBIOS name clients can reach this server by, so a share opens as
    /// `\\NAME\photos` instead of `\\192.168.1.10\photos`. Empty is the
    /// default and means no name at all: `disable netbios = yes`, nothing
    /// listening on 137/138, and only the address works.
    ///
    /// A name only resolves if the sidecar shares the LAN's broadcast domain,
    /// which on Docker means `network_mode: host` or macvlan. Name service is
    /// broadcast UDP and does not cross a bridge.
    pub server_name: String,
    /// Exact addresses or CIDR blocks smbd may bind, or empty for every private
    /// range. Set this when the sidecar runs on the host's network, where it
    /// otherwise binds the LAN, every Docker bridge and any VPN interface at
    /// once: `["192.168.1.10"]` is what the published-port line
    /// `192.168.1.10:445:445` says on a bridged deployment.
    ///
    /// Interface names are rejected. This process cannot see the host's
    /// interfaces, so it cannot tell whether `eth0` there is the LAN or the
    /// uplink, and an unprovable entry is worse than none.
    pub interfaces: Vec<String>,
    /// Shared config volume the sidecar reads.
    pub config_dir: PathBuf,
    /// Where `sc-smb-agent` listens for "apply now" ([`agent`]). Rendering
    /// the four files above is only half of publishing; this is how the other
    /// half reports back, and how a change reaches smbd without waiting for
    /// the agent's own poll.
    ///
    /// Deliberately not under `config_dir`: the agent mounts that read-only,
    /// and a listener has to create its own socket. A deployment with no agent
    /// leaves nothing listening here, which is not an error — the files are
    /// still written and a poll-driven agent still picks them up.
    pub agent_socket: PathBuf,
    /// Samba `force user`/`force group` — the single uid every SMB
    /// connection runs as; real access control is `valid users`/read/write
    /// lists, never Unix permissions.
    pub service_user: String,
    /// Lets the sidecar bind and admit globally-routable addresses, including
    /// a directly-attached IPv6 GUA prefix. Off by default; turning it on
    /// raises a permanent admin-UI warning and an audit event. Reaches the
    /// sidecar through `network.policy`.
    pub allow_public_bind: bool,
    pub totp_policy: TotpPolicy,
}

impl Default for SmbConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            workgroup: "WORKGROUP".to_string(),
            server_name: String::new(),
            interfaces: Vec::new(),
            config_dir: PathBuf::from("/config/smb"),
            agent_socket: PathBuf::from(agent::DEFAULT_SOCKET),
            service_user: "scsvc".to_string(),
            allow_public_bind: false,
            totp_policy: TotpPolicy::default(),
        }
    }
}

/// One Samba `[share]` block. Callers build one of these per distinct
/// subpath grant ("subpath grant") — this crate does
/// not fan a single grant tree out into multiple shares itself, it just
/// renders whatever list it's given.
#[derive(Clone, Debug)]
pub struct SmbShareDef {
    /// Samba share name — the `[name]` section header.
    pub name: String,
    /// Host path as seen by the `sc-smb` sidecar container.
    pub path: String,
    pub valid_users: Vec<String>,
    pub read_list: Vec<String>,
    pub write_list: Vec<String>,
    /// When `true`, another service (Jellyfin, *arr, rsync, ...) can write
    /// the same directory tree, so oplocks are disabled to avoid an SMB
    /// client caching a stale view.
    pub shared_externally: bool,
}

/// An SMB-enabled user, for `getpwnam` passwd-entry synthesis. The
/// `smbpasswd` file content itself comes from `AuthService::export_smbpasswd`
/// — this crate never sees plaintext or NT hashes.
#[derive(Clone, Debug)]
pub struct SmbUser {
    pub name: String,
    /// The uid this account's `passwd` entry carries, and the same value
    /// `sc_auth::export_smbpasswd` writes into its `smbpasswd` line —
    /// `smb.service_uid + <account row id>`.
    ///
    /// **Distinct per account, deliberately.** `pdbedit -i` resolves an
    /// smbpasswd line to a Unix account by uid, not by name, so several names
    /// sharing one uid import as whichever name `getpwuid` answers with —
    /// once, for all of them. Ownership is unaffected: `force user = scsvc`
    /// decides what a connection writes as, so this uid only has to make
    /// `getpwnam` succeed and be unique.
    pub uid: u32,
}

pub struct SmbOrchestrator {
    cfg: SmbConfig,
    /// Set once a config has been rendered under `allow_public_bind = true`.
    /// Sticky for the process lifetime, which is what backs the permanent
    /// warning banner.
    public_bind_warning: AtomicBool,
}

impl SmbOrchestrator {
    pub fn new(cfg: SmbConfig) -> Self {
        Self {
            cfg,
            public_bind_warning: AtomicBool::new(false),
        }
    }

    pub fn config(&self) -> &SmbConfig {
        &self.cfg
    }

    /// `true` once a config has been rendered under `allow_public_bind = true`
    /// in this process's lifetime. The admin UI polls this to render the
    /// permanent warning banner.
    pub fn public_bind_warning_active(&self) -> bool {
        self.public_bind_warning.load(Ordering::SeqCst)
    }

    /// Render `smb.conf` for the given shares/users. Does not touch the
    /// network or filesystem. Every hardening directive in the module docs
    /// is unconditional; nothing here is admin-configurable.
    ///
    /// Latches the public-bind warning when the operator has opted in. The
    /// opt-in itself is the thing worth warning about: whether a public
    /// address exists is only knowable in the sidecar's namespace.
    pub fn generate_conf(
        &self,
        shares: &[SmbShareDef],
        users: &[SmbUser],
    ) -> Result<String, SmbError> {
        let rendered = conf::render(&self.cfg, shares, users)?;
        if self.cfg.allow_public_bind && !self.public_bind_warning.swap(true, Ordering::SeqCst) {
            tracing::warn!(
                target: "audit",
                event = "smb.public_bind_enabled",
                "smb.allow_public_bind is true: SMB may bind a globally routable \
                 address. This is not internet-safe."
            );
        }
        Ok(rendered)
    }

    /// What the sidecar needs from us to decide the network scope. It runs in
    /// the namespace that can see the host's interfaces, so it, not this crate,
    /// does the deciding; these two lines are the whole of its input.
    ///
    /// `pinned_interfaces` is the off switch: the operator named the addresses
    /// in `smb.interfaces`, so the rendered `interfaces`/`hosts allow` are
    /// final and detection must not widen them.
    fn render_network_policy(&self) -> String {
        format!(
            "# Written by sc-server. Read by the sc-smb sidecar/agent.\n\
             allow_public_bind={}\n\
             pinned_interfaces={}\n",
            u8::from(self.cfg.allow_public_bind),
            u8::from(!self.cfg.interfaces.is_empty()),
        )
    }

    /// Convenience: render `/etc/passwd`-style entries for `users`, each on
    /// its own [`SmbUser::uid`] and all on the shared `gid`
    /// (passdb sync point 3).
    pub fn render_passwd_entries(&self, users: &[SmbUser], gid: u32) -> String {
        passwd::render_passwd_entries(users, gid)
    }

    /// Write `smb.conf`, the `smbpasswd`(5) file, passwd entries and
    /// `network.policy` into `cfg.config_dir`. `smbpasswd` gets mode `0600`
    /// (best-effort outside Unix); the sidecar mounts this volume read-only.
    pub fn write_all(&self, conf: &str, smbpasswd: &str, passwd_entries: &str) -> Result<(), SmbError> {
        std::fs::create_dir_all(&self.cfg.config_dir).map_err(|e| SmbError::Io {
            path: self.cfg.config_dir.clone(),
            source: e,
        })?;
        self.write_one("smb.conf", conf, 0o644)?;
        self.write_one("smbpasswd", smbpasswd, 0o600)?;
        self.write_one("passwd", passwd_entries, 0o644)?;
        self.write_one("network.policy", &self.render_network_policy(), 0o644)?;
        Ok(())
    }

    /// Remove the rendered files, if present. Called when `smb.enabled`
    /// goes false: keeping them would leave NT hashes on disk for a disabled
    /// feature, and the bare-metal agent reads their absence as the off
    /// switch rather than as "not synced yet".
    pub fn remove_rendered(&self) -> Result<(), SmbError> {
        for name in ["smb.conf", "smbpasswd", "passwd", "network.policy"] {
            let path = self.cfg.config_dir.join(name);
            match std::fs::remove_file(&path) {
                Ok(()) => {}
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => return Err(SmbError::Io { path, source: e }),
            }
        }
        Ok(())
    }

    fn write_one(&self, name: &str, content: &str, mode: u32) -> Result<(), SmbError> {
        let path = self.cfg.config_dir.join(name);
        std::fs::write(&path, content).map_err(|e| SmbError::Io {
            path: path.clone(),
            source: e,
        })?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            // Best-effort: a failure here (e.g. read-only mount in a test
            // sandbox) shouldn't take down the whole write.
            let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(mode));
        }
        #[cfg(not(unix))]
        {
            let _ = mode;
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn orch(allow_public: bool) -> SmbOrchestrator {
        SmbOrchestrator::new(SmbConfig {
            allow_public_bind: allow_public,
            ..SmbConfig::default()
        })
    }

    #[test]
    fn rendering_without_opt_in_raises_no_warning() {
        let o = orch(false);
        o.generate_conf(&[], &[]).unwrap();
        assert!(!o.public_bind_warning_active());
    }

    #[test]
    fn rendering_under_opt_in_latches_the_warning() {
        let o = orch(true);
        o.generate_conf(&[], &[]).unwrap();
        assert!(o.public_bind_warning_active());
    }

    #[test]
    fn rendered_conf_binds_loopback_only() {
        let conf = orch(true).generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("\n  bind interfaces only = yes\n"));
        assert!(conf.contains("\n  interfaces = lo\n"));
        assert!(conf.contains("\n  hosts allow = 127.0.0.0/8 ::1/128\n"));
        assert!(conf.contains("\n  hosts deny = 0.0.0.0/0\n"));
    }

    #[test]
    fn network_policy_carries_the_opt_in() {
        assert!(orch(false)
            .render_network_policy()
            .contains("allow_public_bind=0"));
        assert!(orch(true)
            .render_network_policy()
            .contains("allow_public_bind=1"));
    }

    #[test]
    fn network_policy_turns_detection_off_for_a_pin() {
        assert!(orch(false)
            .render_network_policy()
            .contains("pinned_interfaces=0"));
        assert!(orch_ifaces(&["192.168.1.10"])
            .render_network_policy()
            .contains("pinned_interfaces=1"));
    }

    fn sample_share(shared_externally: bool) -> SmbShareDef {
        SmbShareDef {
            name: "photos".to_string(),
            path: "/shares/photos".to_string(),
            valid_users: vec!["alice".to_string(), "bob".to_string()],
            read_list: vec!["bob".to_string()],
            write_list: vec!["alice".to_string()],
            shared_externally,
        }
    }

    #[test]
    fn generate_conf_contains_every_hardening_directive() {
        let o = orch(false);
        let conf = o
            .generate_conf(&[sample_share(false)], &[SmbUser { name: "alice".into(), uid: 1001 }])
            .unwrap();

        for directive in [
            "server min protocol = SMB3_11",
            "server signing = required",
            "smb encrypt = required",
            "ntlm auth = ntlmv2-only",
            "lanman auth = no",
            "raw NTLMv2 auth = no",
            "restrict anonymous = 2",
            "null passwords = no",
            "guest ok = no",
            "map to guest = never",
            "unix extensions = no",
            "disable netbios = yes",
            "smb ports = 445",
            "load printers = no",
            "disable spoolss = yes",
            "store dos attributes = no",
            "map archive = no",
            "map hidden = no",
            "map system = no",
            "map readonly = no",
            "ea support = no",
            "bind interfaces only = yes",
            "interfaces = lo",
            "hosts deny = 0.0.0.0/0",
            "hosts allow",
        ] {
            assert!(conf.contains(directive), "missing directive: {directive}\n---\n{conf}");
        }
    }

    #[test]
    fn generate_conf_contains_per_share_fields_and_veto_list() {
        let o = orch(false);
        let conf = o
            .generate_conf(&[sample_share(false)], &[])
            .unwrap();
        assert!(conf.contains("[photos]"));
        assert!(conf.contains("path = /shares/photos"));
        assert!(conf.contains("valid users = alice bob"));
        assert!(conf.contains("read list = bob"));
        assert!(conf.contains("write list = alice"));
        assert!(conf.contains("create mask = 0664"));
        assert!(conf.contains("directory mask = 0775"));
        assert!(conf.contains("veto files = /.sctrash/.scpart-*/.scmeta/.scindex/"));
        assert!(!conf.contains("oplocks = no"));
    }

    #[test]
    fn shared_externally_disables_oplocks() {
        let o = orch(false);
        let conf = o.generate_conf(&[sample_share(true)], &[]).unwrap();
        assert!(conf.contains("oplocks = no"));
    }

    #[test]
    fn one_share_per_entry() {
        let o = orch(false);
        let shares = vec![
            SmbShareDef {
                name: "photos".into(),
                path: "/shares/photos".into(),
                valid_users: vec!["alice".into()],
                read_list: vec![],
                write_list: vec!["alice".into()],
                shared_externally: false,
            },
            SmbShareDef {
                name: "photos-readonly".into(),
                path: "/shares/photos/2024".into(),
                valid_users: vec!["carol".into()],
                read_list: vec!["carol".into()],
                write_list: vec![],
                shared_externally: false,
            },
        ];
        let conf = o.generate_conf(&shares, &[]).unwrap();
        assert!(conf.contains("[photos]"));
        assert!(conf.contains("[photos-readonly]"));
    }

    #[test]
    fn write_all_creates_expected_files() {
        let dir = tempfile::tempdir().unwrap();
        let o = SmbOrchestrator::new(SmbConfig {
            config_dir: dir.path().to_path_buf(),
            ..SmbConfig::default()
        });
        o.write_all("conf-body", "passwd-body", "entries-body").unwrap();
        assert_eq!(std::fs::read_to_string(dir.path().join("smb.conf")).unwrap(), "conf-body");
        assert_eq!(std::fs::read_to_string(dir.path().join("smbpasswd")).unwrap(), "passwd-body");
        assert_eq!(std::fs::read_to_string(dir.path().join("passwd")).unwrap(), "entries-body");
    }

    #[test]
    fn remove_rendered_clears_every_file_and_tolerates_absence() {
        let dir = tempfile::tempdir().unwrap();
        let o = SmbOrchestrator::new(SmbConfig {
            config_dir: dir.path().to_path_buf(),
            ..SmbConfig::default()
        });
        o.write_all("conf-body", "passwd-body", "entries-body")
            .unwrap();
        o.remove_rendered().unwrap();
        for name in ["smb.conf", "smbpasswd", "passwd"] {
            assert!(
                !dir.path().join(name).exists(),
                "{name} survived remove_rendered"
            );
        }
        // Disabling SMB twice must not be an error, and the bare-metal agent
        // polls a directory that may legitimately be empty already.
        o.remove_rendered().unwrap();
    }

    fn orch_named(server_name: &str) -> SmbOrchestrator {
        SmbOrchestrator::new(SmbConfig {
            server_name: server_name.to_string(),
            ..SmbConfig::default()
        })
    }

    #[test]
    fn empty_server_name_leaves_netbios_off() {
        let conf = orch_named("").generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("disable netbios = yes"));
        assert!(!conf.contains("netbios name ="));
    }

    #[test]
    fn server_name_enables_netbios_but_not_port_139() {
        let conf = orch_named("stowcloud").generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("netbios name = stowcloud"));
        assert!(conf.contains("server string = stowcloud"));
        assert!(conf.contains("disable netbios = no"));
        // nmbd serves the name on UDP 137; smbd must stay off the pre-SMB3
        // transport regardless.
        assert!(conf.contains("smb ports = 445"));
    }

    #[test]
    fn server_name_rejects_injection_and_oversize() {
        for bad in ["nas]\n[global", "nas smb", "nas.local", "has-sixteen-chars", "nas;evil"] {
            let err = orch_named(bad).generate_conf(&[], &[]).unwrap_err();
            assert!(
                matches!(err, SmbError::InvalidServerName { .. }),
                "{bad:?} was accepted, got {err:?}"
            );
        }
    }

    #[test]
    fn a_named_server_answers_for_itself_and_nothing_else() {
        // On host networking nmbd shares a broadcast domain with whatever else
        // browses the LAN, and its defaults would have it win elections and
        // forward unknown names to DNS.
        let conf = orch_named("stowcloud").generate_conf(&[], &[]).unwrap();
        for directive in [
            "local master = no",
            "preferred master = no",
            "domain master = no",
            "domain logons = no",
            "os level = 0",
            "wins support = no",
            "dns proxy = no",
        ] {
            assert!(conf.contains(directive), "missing {directive:?}");
        }
    }

    #[test]
    fn multichannel_never_advertises_the_interface_list() {
        let conf = orch(false).generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("server multi channel support = no"));
    }

    fn orch_ifaces(interfaces: &[&str]) -> SmbOrchestrator {
        SmbOrchestrator::new(SmbConfig {
            interfaces: interfaces.iter().map(|s| s.to_string()).collect(),
            ..SmbConfig::default()
        })
    }

    #[test]
    fn empty_interfaces_leaves_the_loopback_baseline_for_detection() {
        let conf = orch_ifaces(&[]).generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("interfaces = lo\n"));
        assert!(conf.contains("hosts allow = 127.0.0.0/8 ::1/128\n"));
    }

    #[test]
    fn explicit_interfaces_replace_the_baseline() {
        let conf = orch_ifaces(&["192.168.1.10", "fd00::1/64"])
            .generate_conf(&[], &[])
            .unwrap();
        assert!(conf.contains("interfaces = lo 192.168.1.10 fd00::1/64"));
        // Narrowing what smbd binds must not narrow who may connect to it: a
        // client at 192.168.1.50 reaches the address above and would be denied.
        assert!(conf.contains("hosts allow = 10.0.0.0/8"));
    }

    #[test]
    fn interfaces_reject_names_and_public_addresses() {
        for bad in ["eth0", "8.8.8.8", "203.0.113.0/24", "192.168.1.0/33"] {
            let err = orch_ifaces(&[bad]).generate_conf(&[], &[]).unwrap_err();
            assert!(
                matches!(err, SmbError::InvalidInterface { .. }),
                "{bad:?} was accepted, got {err:?}"
            );
        }
    }

    #[test]
    fn allow_public_bind_also_frees_the_interface_list() {
        let o = SmbOrchestrator::new(SmbConfig {
            interfaces: vec!["203.0.113.5".to_string()],
            allow_public_bind: true,
            ..SmbConfig::default()
        });
        let conf = o.generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("interfaces = lo 203.0.113.5"));
    }

    #[test]
    fn config_defaults_match_design() {
        let cfg = SmbConfig::default();
        assert!(!cfg.enabled);
        assert!(!cfg.allow_public_bind);
        assert_eq!(cfg.totp_policy, TotpPolicy::RequireSeparate);
    }
}
