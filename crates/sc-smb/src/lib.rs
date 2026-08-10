//! `sc-smb` — Samba (`smbd`) sidecar orchestration.
//!
//! This crate never talks to Samba binaries and never sees plaintext
//! passwords. It only:
//!
//!   1. Enforces the LAN-only bind gate (`validate_bind`) — the control
//!      that justifies sharing the account password with
//!      SMB is a hard refusal, not a warning.
//!   2. Renders `smb.conf` (`generate_conf`) from `Share`/`Grant`-shaped
//!      input the caller already resolved — one Samba `[share]` per distinct
//!      subpath grant, since SMB shares are path-scoped and can't express a
//!      subpath restriction on their own.
//!   3. Writes the three sidecar-facing files (`write_all`): `smb.conf`,
//!      an `smbpasswd`(5) file (rendered by the caller from
//!      `sc_auth::AuthService::export_smbpasswd()` — this crate never
//!      derives or touches NT hashes), and `/etc/passwd`-style entries so
//!      Samba's `getpwnam` succeeds for every SMB user.
//!
//! The sidecar itself (inotify-watch `/config/smb`, `testparm -s`,
//! `smbcontrol reload-config`) is out of scope for this crate — that's a
//! separate small program/container that consumes the files this crate
//! writes.

mod bind;
mod conf;
mod error;
mod passwd;

pub use bind::is_private;
pub use error::SmbError;
pub use passwd::render_passwd_entries;

use std::net::IpAddr;
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
    /// Shared config volume the sidecar reads.
    pub config_dir: PathBuf,
    /// Samba `force user`/`force group` — the single uid every SMB
    /// connection runs as; real access control is `valid users`/read/write
    /// lists, never Unix permissions.
    pub service_user: String,
    /// Escape hatch for `validate_bind`. Off by default; turning it on
    /// raises a permanent admin-UI warning and an audit event.
    pub allow_public_bind: bool,
    pub totp_policy: TotpPolicy,
}

impl Default for SmbConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            workgroup: "WORKGROUP".to_string(),
            server_name: String::new(),
            config_dir: PathBuf::from("/config/smb"),
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
    /// Set once `validate_bind` accepts a public bind under
    /// `allow_public_bind = true`. Sticky for the process lifetime — this
    /// backs the "permanent warning banner" in
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

    /// `true` once a public bind has been accepted under
    /// `allow_public_bind = true` in this process's lifetime. The admin UI
    /// polls this to render the permanent warning banner.
    pub fn public_bind_warning_active(&self) -> bool {
        self.public_bind_warning.load(Ordering::SeqCst)
    }

    /// The hard gate: refuse to proceed if any
    /// interface `smbd` would bind is a public address, unless
    /// `cfg.allow_public_bind` is explicitly `true`. When the override is
    /// used, emits an audit event and latches `public_bind_warning_active`.
    ///
    /// Callers MUST call this — and get `Ok` — before `generate_conf`/
    /// `write_all` run for real; `generate_conf` itself does not re-check
    /// interfaces (it isn't given any).
    pub fn validate_bind(&self, ifaces: &[IpAddr]) -> Result<(), SmbError> {
        let offending = bind::public_addrs(ifaces);
        if offending.is_empty() {
            return Ok(());
        }
        if !self.cfg.allow_public_bind {
            return Err(SmbError::PublicBindRefused { offending });
        }
        self.public_bind_warning.store(true, Ordering::SeqCst);
        tracing::warn!(
            target: "audit",
            event = "smb.public_bind_enabled",
            offending = ?offending,
            "smb.allow_public_bind is true: SMB will bind a public address. \
             This is not internet-safe."
        );
        Ok(())
    }

    /// Render `smb.conf` for the given shares/users. Does not touch the
    /// network or filesystem. Every hardening directive in the module docs
    /// is unconditional; nothing here is admin-configurable.
    pub fn generate_conf(
        &self,
        shares: &[SmbShareDef],
        users: &[SmbUser],
    ) -> Result<String, SmbError> {
        conf::render(&self.cfg, shares, users)
    }

    /// Convenience: render `/etc/passwd`-style entries for `users`, each on
    /// its own [`SmbUser::uid`] and all on the shared `gid`
    /// (passdb sync point 3).
    pub fn render_passwd_entries(&self, users: &[SmbUser], gid: u32) -> String {
        passwd::render_passwd_entries(users, gid)
    }

    /// Write `smb.conf`, the `smbpasswd`(5) file, and passwd entries into
    /// `cfg.config_dir`. `smbpasswd` gets mode `0600` (best-effort outside
    /// Unix); the sidecar mounts this volume read-only.
    pub fn write_all(&self, conf: &str, smbpasswd: &str, passwd_entries: &str) -> Result<(), SmbError> {
        std::fs::create_dir_all(&self.cfg.config_dir).map_err(|e| SmbError::Io {
            path: self.cfg.config_dir.clone(),
            source: e,
        })?;
        self.write_one("smb.conf", conf, 0o644)?;
        self.write_one("smbpasswd", smbpasswd, 0o600)?;
        self.write_one("passwd", passwd_entries, 0o644)?;
        Ok(())
    }

    /// Remove the three rendered files, if present. Called when `smb.enabled`
    /// goes false: keeping them would leave NT hashes on disk for a disabled
    /// feature, and the bare-metal agent reads their absence as the off
    /// switch rather than as "not synced yet".
    pub fn remove_rendered(&self) -> Result<(), SmbError> {
        for name in ["smb.conf", "smbpasswd", "passwd"] {
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
    use std::net::Ipv4Addr;

    fn orch(allow_public: bool) -> SmbOrchestrator {
        SmbOrchestrator::new(SmbConfig {
            allow_public_bind: allow_public,
            ..SmbConfig::default()
        })
    }

    #[test]
    fn validate_bind_accepts_private_only() {
        let o = orch(false);
        let ifaces = [
            IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1)),
            IpAddr::V4(Ipv4Addr::new(192, 168, 1, 10)),
        ];
        assert!(o.validate_bind(&ifaces).is_ok());
        assert!(!o.public_bind_warning_active());
    }

    #[test]
    fn validate_bind_rejects_public_v4_by_default() {
        let o = orch(false);
        let ifaces = [IpAddr::V4(Ipv4Addr::new(203, 0, 113, 5))];
        let err = o.validate_bind(&ifaces).unwrap_err();
        match err {
            SmbError::PublicBindRefused { offending } => {
                assert_eq!(offending, vec![ifaces[0]]);
            }
            other => panic!("expected PublicBindRefused, got {other:?}"),
        }
        assert!(!o.public_bind_warning_active());
    }

    #[test]
    fn validate_bind_rejects_public_v6_by_default() {
        use std::net::Ipv6Addr;
        let o = orch(false);
        let ifaces = [IpAddr::V6(Ipv6Addr::new(0x2001, 0x0db8, 0, 0, 0, 0, 0, 1))];
        assert!(o.validate_bind(&ifaces).is_err());
    }

    #[test]
    fn validate_bind_override_sets_persistent_warning_and_succeeds() {
        let o = orch(true);
        let ifaces = [IpAddr::V4(Ipv4Addr::new(203, 0, 113, 5))];
        assert!(o.validate_bind(&ifaces).is_ok());
        assert!(o.public_bind_warning_active());
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
    fn hosts_allow_lists_every_private_cidr_once() {
        // A `skip(1)` here once dropped 10.0.0.0/8 and duplicated 127.0.0.0/8,
        // so Samba refused every 10.x LAN client before SMB2 NEGOTIATE. The
        // check above only looked for the substring "hosts allow", which is
        // why it went unnoticed until the production sidecar hit it.
        let o = orch(false);
        let conf = o.generate_conf(&[sample_share(false)], &[]).unwrap();
        let line = conf
            .lines()
            .map(str::trim)
            .find(|l| l.starts_with("hosts allow ="))
            .expect("no hosts allow line");
        let cidrs: Vec<&str> = line
            .trim_start_matches("hosts allow =")
            .split_whitespace()
            .collect();
        for expected in [
            "10.0.0.0/8",
            "172.16.0.0/12",
            "192.168.0.0/16",
            "127.0.0.0/8",
            "100.64.0.0/10",
        ] {
            assert_eq!(
                cidrs.iter().filter(|c| **c == expected).count(),
                1,
                "{expected} must appear exactly once in: {line}"
            );
        }
        for expected in ["fc00::/7", "fe80::/10", "::1/128"] {
            assert_eq!(
                cidrs.iter().filter(|c| **c == expected).count(),
                1,
                "{expected} must appear exactly once in: {line}"
            );
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
    fn tailscale_client_is_allowed() {
        // A CGNAT address is what a tailnet peer connects from; treating it as
        // public put every one of them behind `hosts deny`.
        let o = orch(false);
        assert!(o.validate_bind(&[IpAddr::V4(Ipv4Addr::new(100, 101, 102, 103))]).is_ok());
        let conf = o.generate_conf(&[], &[]).unwrap();
        assert!(conf.contains("100.64.0.0/10"));
    }

    #[test]
    fn config_defaults_match_design() {
        let cfg = SmbConfig::default();
        assert!(!cfg.enabled);
        assert!(!cfg.allow_public_bind);
        assert_eq!(cfg.totp_policy, TotpPolicy::RequireSeparate);
    }
}
