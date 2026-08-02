//! `sc-server smb-sync` — render and write `smb.conf`/`smbpasswd`/`passwd`
//! from the live Share/Grant registry, gated by the LAN-only bind check.
//!
//! `DEPLOYMENT.md` §7.3 models Samba access as a projection of the same
//! Share/Grant registry the HTTP layer uses: **one Samba share per distinct
//! grant root**, with `valid users` / `read list` / `write list` derived from
//! who holds which permission bits there. That projection is
//! [`shares_from_registry`] below; the static `[[smb_shares]]` config list
//! remains as an explicit override for deployments that want to hand-write
//! the mapping, and as the fallback when nothing is registered at all.

use std::collections::BTreeMap;

use crate::config::Config;

pub fn run(cfg: &Config, master_key: &[u8; 32]) -> anyhow::Result<()> {
    let orch = sc_smb::SmbOrchestrator::new(cfg.smb.clone());

    if !cfg.smb.enabled {
        orch.remove_rendered()?;
        println!("smb: disabled (smb.enabled = false); removed any rendered config");
        return Ok(());
    }

    let ifaces = crate::diagnostics::local_interface_addrs();
    orch.validate_bind(&ifaces)?;

    // `smbpasswd` content comes from `sc-auth`, which is the only place that
    // ever holds the NT hash and the master key needed to decrypt it
    // (`DEPLOYMENT.md` §7.2 passdb sync). `sc-smb` never sees plaintext or NT
    // hashes.
    let auth_db_path = cfg.data_dir.join("auth.db");
    let auth_cfg = sc_auth::AuthConfig {
        smb_totp_policy: match cfg.smb.totp_policy {
            sc_smb::TotpPolicy::RequireSeparate => sc_auth::SmbTotpPolicy::RequireSeparate,
            sc_smb::TotpPolicy::Block => sc_auth::SmbTotpPolicy::Block,
        },
        ..sc_auth::AuthConfig::default()
    };
    let auth = sc_auth::AuthService::new(&auth_db_path, auth_cfg, *master_key)?;

    // Explicit `[[smb_shares]]` wins when present: an admin who wrote the
    // mapping by hand meant it, and silently unioning it with a derived list
    // would produce shares nobody asked for.
    let (shares, overgrants, source) = if !cfg.smb_shares.is_empty() {
        // A hand-written mapping is the admin's own literal `smb.conf`
        // content; there is no registry intent behind it to be wider than.
        (
            cfg.smb_shares.iter().map(Into::into).collect::<Vec<_>>(),
            Vec::new(),
            "static [[smb_shares]]",
        )
    } else {
        let (s, o) = shares_from_registry(cfg, &auth)?;
        (s, o, "Share/Grant registry")
    };

    // Users referenced by any share's valid/read/write list, deduplicated,
    // for the passwd(5)-style entries every SMB user needs (`getpwnam`).
    let mut user_names: Vec<String> = Vec::new();
    for s in &shares {
        for n in s
            .valid_users
            .iter()
            .chain(&s.read_list)
            .chain(&s.write_list)
        {
            if !user_names.contains(n) {
                user_names.push(n.clone());
            }
        }
    }
    let users = smb_users(&auth, &user_names, cfg.smb_service_uid)?;

    let conf = orch.generate_conf(&shares, &users)?;
    // Both files derive their uid the same way, from the same row ids: they
    // have to agree per account, or `pdbedit -i` imports that account as
    // nothing — see `export_smbpasswd`.
    let smbpasswd = auth.export_smbpasswd(cfg.smb_service_uid)?;
    let passwd_entries = orch.render_passwd_entries(&users, cfg.smb_service_gid);

    orch.write_all(&conf, &smbpasswd, &passwd_entries)?;

    if orch.public_bind_warning_active() {
        tracing::warn!("smb: bound to a public address under allow_public_bind=true — see the admin UI warning banner");
    }
    log_overgrants(&overgrants);

    println!(
        "smb: wrote {} ({} share(s) from {}, {} user(s))",
        cfg.smb.config_dir.display(),
        shares.len(),
        source,
        users.len()
    );
    Ok(())
}

/// One audit line per grant Samba is about to render wider than the registry
/// means it. `target: "audit"` for the same reason `smb.public_bind_enabled`
/// uses it (`sc_smb::validate_bind`): this is a standing property of the
/// deployment's access control, not a transient event, and an operator has to
/// be able to find it after the fact.
fn log_overgrants(overgrants: &[SmbOvergrant]) {
    for o in overgrants {
        tracing::warn!(
            target: "audit",
            event = o.kind_key(),
            share = %o.share,
            user = %o.user,
            detail = ?o.detail(),
            "smb: this grant is rendered more permissively than the registry defines it; smb.conf cannot express the difference"
        );
    }
}

/// Project the Share/Grant registry onto Samba shares.
///
/// One `[section]` per distinct grant root — not one per *share* — because a
/// grant on a subdirectory is precisely the case Samba cannot express any
/// other way: `smb.conf` has no per-path ACL, so a subpath grant has to
/// become its own share rooted at that path (`DEPLOYMENT.md` §7.3, "subpath
/// grant"). `AclEngine::roots` already computes exactly that projection —
/// including the label de-duplication — for the web UI, so reusing it keeps
/// the two views of "what can this user reach" from drifting.
///
/// A user lands in `write list` when their effective permissions there
/// include any mutating bit, and in `read list` otherwise. Everyone named in
/// either is also in `valid users`: that is Samba's gate, and the two lists
/// only choose between read-only and read-write behind it.
///
/// Users who cannot use SMB at all (`disabled`, `smb_opt_out`, or no
/// `smb_enabled` flag) are omitted entirely rather than listed and rejected
/// at connect time — `smb.conf` is also documentation of who has access.
fn shares_from_registry(
    cfg: &Config,
    auth: &sc_auth::AuthService,
) -> anyhow::Result<(Vec<sc_smb::SmbShareDef>, Vec<SmbOvergrant>)> {
    // Rebuild the same domain state `serve` builds, from the same functions,
    // so the exported view and the served view cannot disagree.
    let meta = std::sync::Arc::new(sc_meta::MetaStore::open(&cfg.data_dir.join("meta.db"))?);
    let acl = std::sync::Arc::new(sc_acl::AclEngine::new());
    let core = sc_core::Core::new(meta, acl.clone());
    // Grants are persisted in their own database now (`sc_core::acl_store`),
    // not recomputed from the account list — without attaching the same
    // `acl.db` the real server uses, `project_grants` below would see no
    // grants at all and this command would silently export zero shares for
    // every user.
    core.attach_acl_store(sc_core::AclStore::open(&cfg.data_dir.join("acl.db"))?)?;
    crate::app::register_shares(&core, cfg)?;
    crate::app::project_grants(&core, &acl, auth)?;
    project_registry_shares(&core, auth)
}

/// Any bit that lets a user change something. `SHARE`/`DOWNLOAD` are
/// deliberately not here: neither has an `smb.conf` counterpart.
const WRITE_BITS: sc_acl::Perms = sc_acl::Perms::WRITE
    .union(sc_acl::Perms::CREATE)
    .union(sc_acl::Perms::DELETE)
    .union(sc_acl::Perms::RENAME)
    .union(sc_acl::Perms::MOVE);

/// A grant this deployment holds that Samba is about to render **more
/// permissively than the registry means it**, because `smb.conf` cannot say
/// what the registry says.
///
/// Not a refusal. Both shapes are legitimate web-side configurations, and
/// turning SMB off for the whole deployment because one grant is finer than
/// SMB's vocabulary would be a worse answer than saying so. But neither may
/// be silent: an admin who wrote "read and write, no deleting" in the UI has
/// not been told SMB ignores the second half, and would have no way to find
/// out short of trying it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SmbOvergrant {
    /// The rendered `smb.conf` section this concerns.
    pub share: String,
    pub user: String,
    pub kind: SmbOvergrantKind,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SmbOvergrantKind {
    /// Samba's `write list` is all-or-nothing: any one mutating bit grants
    /// every one of them. Carries the bit names the registry withholds and
    /// SMB will hand over anyway.
    WriteListGrantsMore { also_granted: Vec<&'static str> },
    /// A deny below the share root. `smb.conf` has no per-path ACL, so the
    /// share exposes the whole tree and the deny does not apply over SMB.
    DenyBelowRootIgnored { subpaths: Vec<String> },
}

impl SmbOvergrant {
    /// Stable identifier for the wire and for logs — the UI turns it into a
    /// sentence, this crate never does (`scripts/verify.sh` "no Korean in
    /// crates/*/src": a refusal travels as a catalogue key, not as prose).
    pub fn kind_key(&self) -> &'static str {
        match self.kind {
            SmbOvergrantKind::WriteListGrantsMore { .. } => "smb.write_list_grants_more",
            SmbOvergrantKind::DenyBelowRootIgnored { .. } => "smb.deny_below_root_ignored",
        }
    }

    /// The offending detail, already rendered: permission names for the
    /// first kind, subpaths for the second.
    pub fn detail(&self) -> Vec<String> {
        match &self.kind {
            SmbOvergrantKind::WriteListGrantsMore { also_granted } => {
                also_granted.iter().map(|s| (*s).to_string()).collect()
            }
            SmbOvergrantKind::DenyBelowRootIgnored { subpaths } => subpaths.clone(),
        }
    }
}

/// `WRITE_BITS`, by name, for [`SmbOvergrantKind::WriteListGrantsMore`].
const WRITE_BIT_NAMES: [(sc_acl::Perms, &str); 5] = [
    (sc_acl::Perms::WRITE, "write"),
    (sc_acl::Perms::CREATE, "create"),
    (sc_acl::Perms::DELETE, "delete"),
    (sc_acl::Perms::RENAME, "rename"),
    (sc_acl::Perms::MOVE, "move"),
];

/// The projection-only half of [`shares_from_registry`]: given an already-
/// populated `core` (grants attached, shares registered), fold its Share/
/// Grant registry into Samba share defs. Split out so [`render_live`] can
/// reuse the server's own live, already-open `Core`/`AuthService` — no fresh
/// SQLite connections — for in-process `smb.conf` regeneration triggered by
/// an admin settings change, without a restart.
fn project_registry_shares(
    core: &sc_core::Core,
    auth: &sc_auth::AuthService,
) -> anyhow::Result<(Vec<sc_smb::SmbShareDef>, Vec<SmbOvergrant>)> {
    // label -> (host path, write list, read list)
    let mut sections: BTreeMap<String, (String, Vec<String>, Vec<String>)> = BTreeMap::new();
    let mut overgrants: Vec<SmbOvergrant> = Vec::new();

    for u in auth
        .list_users()?
        .iter()
        .filter(|u| !u.disabled && u.smb_enabled && !u.smb_opt_out)
    {
        for root in core.roots(u.id) {
            let Some(mut path) = core.share_host_path(root.share) else {
                continue;
            };
            for comp in root.subpath.components() {
                path.push(comp.as_str());
            }
            let entry = sections
                .entry(root.label.clone())
                .or_insert_with(|| (path.display().to_string(), Vec::new(), Vec::new()));
            if root.perms.intersects(WRITE_BITS) {
                entry.1.push(u.name.clone());

                // In `write list`, so Samba grants every mutating operation.
                // Report the ones the registry withholds.
                let also: Vec<&'static str> = WRITE_BIT_NAMES
                    .iter()
                    .filter(|(bit, _)| !root.perms.contains(*bit))
                    .map(|(_, name)| *name)
                    .collect();
                if !also.is_empty() {
                    overgrants.push(SmbOvergrant {
                        share: root.label.clone(),
                        user: u.name.clone(),
                        kind: SmbOvergrantKind::WriteListGrantsMore { also_granted: also },
                    });
                }
            } else {
                entry.2.push(u.name.clone());
            }

            // The share is the whole tree under `root`; a deny inside it has
            // nowhere to go in `smb.conf`.
            let denied = core.denies_below(u.id, root.share, &root.subpath);
            if !denied.is_empty() {
                overgrants.push(SmbOvergrant {
                    share: root.label.clone(),
                    user: u.name.clone(),
                    kind: SmbOvergrantKind::DenyBelowRootIgnored { subpaths: denied },
                });
            }
        }
    }

    let shares = sections
        .into_iter()
        .map(|(name, (path, write_list, read_list))| {
            let mut valid_users = write_list.clone();
            valid_users.extend(read_list.iter().cloned());
            valid_users.sort();
            valid_users.dedup();
            sc_smb::SmbShareDef {
                name,
                path,
                valid_users,
                read_list,
                write_list,
                // Whether a share is reachable from outside the LAN is a
                // deployment fact `sc-smb` warns about; the registry does not
                // model it, so never claim it here.
                shared_externally: false,
            }
        })
        .collect::<Vec<_>>();

    Ok((shares, overgrants))
}

/// Regenerate and write `smb.conf`/`smbpasswd`/`passwd` from the server's own
/// live, already-running `Core`/`AuthService` — the in-process counterpart of
/// `run()` above (which is the CLI path and opens fresh DB connections).
/// Called by the settings-screen SMB patch handler so a workgroup/service-
/// user/totp-policy change (anything except `smb.enabled` itself, which is
/// baked into `CoreBridge` at boot and needs a real restart) takes effect
/// immediately, with no restart and no separate `smb-sync` invocation.
///
/// `core`/`auth` already have `register_shares`/`project_grants` applied —
/// that happens once at `App::build` time — so this does not re-run them.
///
/// Returns what the settings screen has to show about this render: whether
/// the LAN-only bind gate was crossed under `smb.allow_public_bind = true`
/// (`sc_smb::SmbOrchestrator::public_bind_warning_active`, which cannot be
/// read back here since this function's `orch` is dropped at the end of the
/// call), and every grant Samba is about to widen.
pub fn render_live(
    cfg: &Config,
    core: &sc_core::Core,
    auth: &sc_auth::AuthService,
) -> anyhow::Result<RenderOutcome> {
    let orch = sc_smb::SmbOrchestrator::new(cfg.smb.clone());
    if !cfg.smb.enabled {
        orch.remove_rendered()?;
        return Ok(RenderOutcome::default());
    }
    let ifaces = crate::diagnostics::local_interface_addrs();
    orch.validate_bind(&ifaces)?;

    let (shares, overgrants) = if !cfg.smb_shares.is_empty() {
        (
            cfg.smb_shares.iter().map(Into::into).collect::<Vec<_>>(),
            Vec::new(),
        )
    } else {
        project_registry_shares(core, auth)?
    };

    let mut user_names: Vec<String> = Vec::new();
    for s in &shares {
        for n in s.valid_users.iter().chain(&s.read_list).chain(&s.write_list) {
            if !user_names.contains(n) {
                user_names.push(n.clone());
            }
        }
    }
    let users = smb_users(auth, &user_names, cfg.smb_service_uid)?;

    let conf = orch.generate_conf(&shares, &users)?;
    // Both files derive their uid the same way, from the same row ids — see
    // `render` above and `export_smbpasswd`.
    let smbpasswd = auth.export_smbpasswd(cfg.smb_service_uid)?;
    let passwd_entries = orch.render_passwd_entries(&users, cfg.smb_service_gid);
    orch.write_all(&conf, &smbpasswd, &passwd_entries)?;
    log_overgrants(&overgrants);
    Ok(RenderOutcome {
        public_bind_warning: orch.public_bind_warning_active(),
        overgrants,
    })
}

/// What one `render_live` call has to tell the settings screen.
#[derive(Clone, Debug, Default)]
pub struct RenderOutcome {
    pub public_bind_warning: bool,
    pub overgrants: Vec<SmbOvergrant>,
}

/// Pair each name with the uid its `passwd` entry gets: `base_uid` plus the
/// account's row id, which is exactly what `export_smbpasswd` writes into the
/// matching `smbpasswd` line. The two renderers must not compute this
/// independently — one deriving it differently from the other is the whole
/// class of bug this shape exists to close — so this is the only place that
/// knows the formula on the passwd side.
///
/// A name with no account behind it can only come from a hand-written
/// `[[smb_shares]]` entry naming somebody who does not exist. It gets no
/// passwd entry and no smbpasswd line, so Samba refuses it at connect time
/// rather than resolving it to whatever `getpwuid` happened to answer.
fn smb_users(
    auth: &sc_auth::AuthService,
    names: &[String],
    base_uid: u32,
) -> anyhow::Result<Vec<sc_smb::SmbUser>> {
    let rows = auth.list_users()?;
    let mut out = Vec::with_capacity(names.len());
    for name in names {
        match rows.iter().find(|r| &r.name == name) {
            Some(row) => out.push(sc_smb::SmbUser {
                name: name.clone(),
                uid: base_uid.saturating_add(row.id.get()),
            }),
            None => tracing::warn!(
                user = %name,
                "smb: a configured share names an account that does not exist; it gets no passwd entry and cannot connect"
            ),
        }
    }
    Ok(out)
}
